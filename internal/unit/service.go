package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/progress"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
)

type Service struct {
	store              *Store
	sourceStore        *source.Store
	llmClient          llm.LLMClient
	unitsIndex         bleve.Index
	pointsIndex        bleve.Index
	queue              *queue.Queue
	cfg                *config.Config
	broadcaster        *progress.Broadcaster
	activationNotifier ActivationNotifier
	conceptNotifier    EntryNotifier
	wikiEntryNotifier  WikiEntryNotifier

	// extracting guards against two Extract runs on the same source at once
	// (double-triggered queue tasks, a retry racing the original) — the second
	// run would re-insert the whole document's units next to the first's.
	extractMu  sync.Mutex
	extracting map[string]bool
}

// ErrExtractionInProgress is returned by Extract when the same source
// already has an extraction running. The queue handler treats it as "leave
// units_status alone" — the in-flight run owns that status.
var ErrExtractionInProgress = errors.New("unit: extraction already in progress")

// ActivationNotifier lets the Activation module's Matcher learn about KP
// lifecycle changes, since its matchable-link cache only holds links whose
// target KP is lifecycle=current (docs/impl/v1/activation.md 步骤 2 候选加载).
// SetUnitLifecycle no-ops when unset.
//
// NotifyPointsLifecycleChanged (2026-08-13, docs/impl/v1/activation.md「依赖」
// Lifecycle) is the direct-write path that replaced Study's old indirect
// "感知后生成降权信号" mechanism: for each pointID with a non-deprecated
// existing link, re-derive its status — the lifecycle check inside that
// derivation handles both directions (KP went non-current → link becomes
// deprecated; KP restored → link re-derives from its conditions) through one
// code path (activation.Service.deriveAndPersistStatus). InvalidateCache is
// still called separately (by SetUnitLifecycle below) since lifecycle
// changes affect Match's candidate pool independent of any single link's
// status derivation.
type ActivationNotifier interface {
	InvalidateCache() error
	NotifyPointsLifecycleChanged(pointIDs []string) error
}

// EntryNotifier lets cross-Source KPN matching (kpn_cross.go,
// docs/impl/v1/kpn.md 步骤 3) hand entry_id-empty KP clusters to the
// concept evolution module instead of falling back to a same-domain
// candidate pool — satisfied by *entry.Service without a direct import
// (concept already depends on wiki; keeping unit -> concept one-directional
// via this interface, like WikiNotifier/ActivationNotifier above, avoids
// unit's public surface hard-requiring the concept package in tests).
// SetEntryNotifier no-ops when unset.
type EntryNotifier interface {
	// kind is the concept/fact classification (docs/impl/v1/kpn.md 步骤 3,
	// from unit_kind_classify on the direct-match path, or forced
	// "concept"/"fact" on the orphan leftover paths) — validated by the
	// concept module. suggestedBoundary is kpn_entry_propose.md's
	// suggested_boundary (2026-08-05). entity is only meaningful when
	// kind=="fact" (empty for concept clusters) — lets the concept module
	// track/accumulate alias names across merges (docs/impl/v1/kpn.md 步骤
	// 3, 2026-08-05).
	// parentEntryID is the concept entry_id a fact candidate was classified
	// under (kpn.md 步骤 3 fact 新建), empty for concept clusters — persisted
	// verbatim as entries.parent_entry_id on confirm (fact-entry-parent-
	// concept-task-brief.md).
	ProposeAddCandidate(domainID, suggestedName, suggestedDescription, suggestedBoundary, kind, entity string, pointIDs []string, sourceID, parentEntryID string) (candidateID string, err error)
	// ListActiveEntryReferences returns one formatted "name：description｜
	// 边界：boundary" line per existing entry in the domain, used as a
	// granularity/abstraction-level reference (docs/impl/v1/kpn.md 步骤 3) so
	// the LLM names new content_driven entries at the same level instead of
	// drifting to overly specific, scenario-bound names — description and
	// boundary give it concrete examples of "what abstraction level looks
	// like here", not just a bare name to pattern-match against.
	ListActiveEntryReferences(domainID string) ([]string, error)
	// ListActiveConceptEntryReferences returns one "entry_id\tname\t
	// description\tboundary" line per existing kind=concept entry in the
	// domain — kpn_fact_concept_match.md's (docs/impl/v1/kpn.md 步骤 3
	// 二阶段, 2026-08-05) match candidate list for combining a kind=fact
	// cluster's entity with an existing concept into an "entity+concept"
	// name. An empty result (no concepts yet in this domain) skips stage 2
	// entirely — cost control, not an error.
	ListActiveConceptEntryReferences(domainID string) ([]string, error)
	// ListPendingAddPointIDs returns point_ids already attached to
	// pending_confirm kind=add candidates in domainID — orphan proposal
	// skips these so a 知识领域 "+ 新增词条" re-run does not re-cluster
	// material that is already waiting for human confirm.
	ListPendingAddPointIDs(domainID string) ([]string, error)
}

// WikiEntryNotifier lets the Wiki module learn that an entry_id's Core KP
// composition just changed, so it can flag any published page compiled from
// that entry_id needs_recompile (docs/impl/v1/wiki.md「重编译标记」). This
// re-wires the two of the design's three automatic sources that don't
// depend on question-answering confidence accumulation — "a. lifecycle 传导"
// (a KU's lifecycle flip moves a KP into/out of an entry's Core) and the new
// "entry_id 归属变化" scenario (a KU gets classified into/out of an entry) —
// after both were dropped along with the two-tier architecture's automatic
// candidate identification (2026-08-18 单层化收尾, docs/impl/v1/
// wiki-single-tier-open-questions.md「已拍板」). The other two design
// sources ("b. Study 周期扫描新增 qualifying KP" and "d. ActivationLink
// verified") are deliberately NOT restored — both key off accumulated
// answer confidence, which conflicts with this revision's "编译准入不再
// 依赖置信度" direction; see the CLAUDE.md 任务说明 for this decision.
//
// Deliberately not the old (deleted) WikiNotifier shape — that interface's
// two methods were "point_id lifecycle changed" and "link verified" and
// would silently reintroduce source d if reused verbatim. This one takes
// entry_id, not point_id/link_id, and only decides "does this entry_id's
// Core-membership page need re-flagging", nothing about individual
// links/points. SetWikiEntryNotifier no-ops when unset.
type WikiEntryNotifier interface {
	// NotifyEntriesChanged marks needs_recompile on every published page
	// compiled from any of entryIDs (matched via wiki_page_entries — Core
	// membership only, no Context/Conflict one-hop expansion, per
	// docs/impl/v1/wiki-single-tier-open-questions.md「已拍板」: cheap
	// enough to call on every lifecycle/entry_id event). Duplicate/empty
	// ids and non-published pages are silently skipped.
	NotifyEntriesChanged(entryIDs []string, reason string) error
}

func (s *Service) SetEntryNotifier(n EntryNotifier) {
	s.conceptNotifier = n
}

func (s *Service) SetActivationNotifier(n ActivationNotifier) {
	s.activationNotifier = n
}

func (s *Service) SetWikiEntryNotifier(n WikiEntryNotifier) {
	s.wikiEntryNotifier = n
}

// notifyWikiEntriesChanged is the shared best-effort call site every entry_id
// mutation (lifecycle transitions, concept (re)matching) funnels through —
// nil-safe, empty-slice-safe, logs and swallows the error like every other
// cross-module notifier in this file (ActivationNotifier/EntryNotifier
// above) so a Wiki-side hiccup never fails the KU/KP write that triggered it.
func (s *Service) notifyWikiEntriesChanged(entryIDs []string, reason string) {
	if s.wikiEntryNotifier == nil || len(entryIDs) == 0 {
		return
	}
	if err := s.wikiEntryNotifier.NotifyEntriesChanged(entryIDs, reason); err != nil {
		slog.Warn("unit: wiki entry notify failed", "error", err)
	}
}

func NewService(store *Store, sourceStore *source.Store, llmClient llm.LLMClient, unitsIdx, pointsIdx bleve.Index, q *queue.Queue, cfg *config.Config) *Service {
	return &Service{
		store:       store,
		sourceStore: sourceStore,
		llmClient:   llmClient,
		unitsIndex:  unitsIdx,
		pointsIndex: pointsIdx,
		queue:       q,
		cfg:         cfg,
		extracting:  make(map[string]bool),
	}
}

// beginExtract marks sourceID as having an extraction in flight; false means
// one is already running and the caller must bail with ErrExtractionInProgress.
func (s *Service) beginExtract(sourceID string) bool {
	s.extractMu.Lock()
	defer s.extractMu.Unlock()
	if s.extracting[sourceID] {
		return false
	}
	s.extracting[sourceID] = true
	return true
}

func (s *Service) endExtract(sourceID string) {
	s.extractMu.Lock()
	delete(s.extracting, sourceID)
	s.extractMu.Unlock()
}

func (s *Service) SetBroadcaster(b *progress.Broadcaster) {
	s.broadcaster = b
}

func (s *Service) emit(sourceID string, evt progress.Event) {
	if s.broadcaster != nil {
		s.broadcaster.Emit(sourceID, evt)
	}
}

func (s *Service) TriggerExtract(sourceID string) error {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get source: %w", err)
	}
	if src.Status != "completed" {
		return fmt.Errorf("unit: source %s status is %q, need completed", sourceID, src.Status)
	}

	s.queue.Enqueue(queue.Task{
		Type:    queue.TaskTypeUnitExtract,
		Payload: queue.UnitTask{SourceID: sourceID},
	})
	return nil
}

// SourceCoverageReport recomputes the same segments Extract used
// (outlines + markdown → BuildSegments) and diffs them against the units
// already in SQLite — read-only, no LLM calls — to surface any segment
// lines that ended up in no completed unit at all (docs/impl/mvp/unit.md
// 完成标准 doesn't cover this yet; see ComputeCoverage for what counts).
func (s *Service) SourceCoverageReport(sourceID string) ([]SegmentCoverage, error) {
	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: get source: %w", err)
	}

	outlines, err := s.sourceStore.GetOutlines(sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit: get outlines: %w", err)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return nil, fmt.Errorf("unit: read markdown: %w", err)
	}
	mdLines := strings.Split(string(mdBytes), "\n")

	segments := BuildSegments(outlines, mdLines, s.cfg.Source.SegmentMaxChars, s.cfg.Source.MinSegmentChars)

	// Only current-lifecycle units represent the document's present content;
	// a reuploaded source's superseded units must not count toward coverage.
	units, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		return nil, fmt.Errorf("unit: get units: %w", err)
	}

	return ComputeCoverage(segments, units, mdLines), nil
}

func (s *Service) Extract(ctx context.Context, sourceID string) error {
	if !s.beginExtract(sourceID) {
		return fmt.Errorf("%w: source %s", ErrExtractionInProgress, sourceID)
	}
	defer s.endExtract(sourceID)
	if err := s.sourceStore.StartUnitsProcessing(sourceID); err != nil {
		return fmt.Errorf("unit: start processing: %w", err)
	}

	src, err := s.sourceStore.GetByID(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get source: %w", err)
	}

	outlines, err := s.sourceStore.GetOutlines(sourceID)
	if err != nil {
		return fmt.Errorf("unit: get outlines: %w", err)
	}

	mdBytes, err := os.ReadFile(src.MarkdownPath)
	if err != nil {
		return fmt.Errorf("unit: read markdown: %w", err)
	}
	mdLines := strings.Split(string(mdBytes), "\n")

	segments := BuildSegments(outlines, mdLines, s.cfg.Source.SegmentMaxChars, s.cfg.Source.MinSegmentChars)
	if len(segments) == 0 {
		slog.Warn("unit: no segments to extract", "source_id", sourceID)
		return nil
	}

	s.emit(sourceID, progress.Event{Step: progress.StepUnitSegment, Status: progress.StatusCompleted, Message: fmt.Sprintf("切分为 %d 段", len(segments)), Total: len(segments)})

	extractStart := time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepUnitExtract, Status: progress.StatusStarted, Message: fmt.Sprintf("并发提取知识单元 (0/%d)", len(segments)), Current: 0, Total: len(segments)})
	done := 0
	semanticsStart := time.Now()
	onBeforeSemantics := func() error {
		if err := s.sourceStore.MarkUnitsSemanticsStarted(sourceID); err != nil {
			return err
		}
		s.emit(sourceID, progress.Event{Step: progress.StepUnitSemantics, Status: progress.StatusStarted, Message: "提取知识单元语义"})
		semanticsStart = time.Now()
		return nil
	}
	if err := s.extractSegmentsPreInsertDedup(ctx, src.Title, src.Summary.String, sourceID, segments, mdLines, func() {
		done++
		s.emit(sourceID, progress.Event{Step: progress.StepUnitExtract, Status: progress.StatusCompleted, Message: fmt.Sprintf("提取知识单元 (%d/%d)", done, len(segments)), Current: done, Total: len(segments), ElapsedMs: time.Since(extractStart).Milliseconds()})
	}, onBeforeSemantics); err != nil {
		return fmt.Errorf("unit: extract and publish: %w", err)
	}
	stepStart := time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepKPNGenerate, Status: progress.StatusStarted, Message: "KPN 关系生成"})
	s.generateKPN(ctx, sourceID)
	s.emit(sourceID, progress.Event{Step: progress.StepKPNGenerate, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepEntryMatch, Status: progress.StatusStarted, Message: "概念匹配"})
	s.matchEntries(ctx, sourceID, src.DomainID)
	s.emit(sourceID, progress.Event{Step: progress.StepEntryMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	// Step 6 (docs/impl/v1/kpn.md): cross-Source KPN matching. Failure is
	// isolated — it must never fail the Source's own completion status.
	stepStart = time.Now()
	s.emit(sourceID, progress.Event{Step: progress.StepKPNCrossMatch, Status: progress.StatusStarted, Message: "跨源 KPN 匹配"})
	if _, err := s.CrossSourceKPN(ctx, sourceID); err != nil {
		slog.Warn("unit: cross source kpn failed", "source_id", sourceID, "error", err)
	}
	s.emit(sourceID, progress.Event{Step: progress.StepKPNCrossMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

	s.emit(sourceID, progress.Event{Step: progress.StepUnitSemantics, Status: progress.StatusCompleted, Message: "知识单元语义已发布", ElapsedMs: time.Since(semanticsStart).Milliseconds()})
	s.emit(sourceID, progress.Event{Step: "done", Status: progress.StatusCompleted, Message: "处理完成"})

	return nil
}

// Prompt versions recorded on knowledge_units.prompt_version. These MUST
// match the `version:` frontmatter of the corresponding config/prompts/*.md
// template — TestPromptVersionConstantsMatchTemplates enforces it, because
// the two drifted once (retry template moved to v6 while the code kept
// stamping "v5") and the stale stamps made real duplicate twins look like
// they came from different extraction generations.
const (
	promptVersionExtractRetry = "v7" // unit_extract_retry.md
	promptVersionGapExtract   = "v1" // unit_gap_extract.md
	promptVersionKPNExtract   = "v2" // kpn_extract.md
	promptVersionKPNCross     = "v4" // kpn_cross_match.md
)

type llmUnit struct {
	UnitID          string `json:"unit_id"`
	Center          string `json:"center"`
	LineStart       int    `json:"line_start"`
	FirstLineAnchor string `json:"first_line_anchor"`
	LineEnd         int    `json:"line_end"`
	LastLineAnchor  string `json:"last_line_anchor"`
}

type llmPoint struct {
	PointID         string `json:"point_id"`
	UnitID          string `json:"unit_id"`
	Content         string `json:"content"`
	Type            string `json:"type"`
	LineStart       int    `json:"line_start"`
	FirstLineAnchor string `json:"first_line_anchor"`
	LineEnd         int    `json:"line_end"`
	LastLineAnchor  string `json:"last_line_anchor"`
	// SourceTheme/ContentTheme/Object/Scope are this point's own rerank
	// semantics (docs/impl/v1/semantics-curation.md 2026-08-21 改判: 下沉自
	// KU 级 unit_rerank_semantics 到 KP 级) — a KU's points can each apply to
	// a different object/scope, so these travel per point, not per unit.
	// SemanticsPromptVersion is set only when ContentTheme/Scope were
	// actually produced by this point's own extraction call (a required
	// field came back empty for gap/retry/coverage-fill paths that don't yet
	// populate these — see kp_semantics.go's backfill for closing that gap).
	SourceTheme            string `json:"-"`
	ContentTheme           string `json:"-"`
	Object                 string `json:"-"`
	Scope                  string `json:"-"`
	SemanticsPromptVersion string `json:"-"`
}

type extractOutput struct {
	Units  []llmUnit  `json:"units"`
	Points []llmPoint `json:"points"`
}

func (s *Service) validateUnit(u llmUnit, points []llmPoint) bool {
	if u.Center == "" {
		return false
	}
	hasPoints := false
	for _, p := range points {
		if p.UnitID == u.UnitID {
			if p.Content == "" {
				return false
			}
			hasPoints = true
		}
	}
	return hasPoints
}

// retryFailedUnit re-runs a failed unit's whole segment through
// unit_extract_retry.md and returns the first locatable, valid unit as an
// in-memory candidate — no store writes, so the pre-insert pipeline can pool
// it with everything else instead of it bypassing dedup (the old
// direct-insert here was the path that put v5/v6 duplicate twins in the
// database). A failed retry is logged but never written: all generation rows
// remain behind PublishGeneration's transaction boundary.
func (s *Service) retryFailedUnit(ctx context.Context, sourceID string, seg Segment, segIndex int, u llmUnit, mdLines []string) (unitCandidate, bool) {
	vars := map[string]string{
		"segment_line_start": strconv.Itoa(seg.LineStart),
		"segment_line_end":   strconv.Itoa(seg.LineEnd),
		"segment_line_count": strconv.Itoa(seg.LineEnd - seg.LineStart + 1),
		"text_content":       sliceLinesWithLineNumbers(mdLines, seg.LineStart, seg.LineEnd),
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_extract_retry.md", vars, "extraction")
	if err != nil {
		logFailedUnitRetry(sourceID, seg, u, "retry LLM call failed: "+err.Error())
		return unitCandidate{}, false
	}

	var output extractOutput
	if err := json.Unmarshal(data, &output); err != nil {
		logFailedUnitRetry(sourceID, seg, u, "retry JSON parse failed: "+err.Error())
		return unitCandidate{}, false
	}

	for _, ru := range output.Units {
		lineStart, lineEnd, _, locateOK := LocateUnitBounds(mdLines, seg, ru.LineStart, ru.FirstLineAnchor, ru.LineEnd, ru.LastLineAnchor, seg.LineStart)
		if !locateOK || !s.validateUnit(ru, output.Points) {
			continue
		}

		var unitPoints []llmPoint
		for _, p := range output.Points {
			if p.UnitID == ru.UnitID {
				unitPoints = append(unitPoints, p)
			}
		}
		lineStart, lineEnd = WidenBoundsFromPoints(mdLines, seg, lineStart, lineEnd, unitPoints)

		return unitCandidate{
			id:            uuid.New().String(),
			llm:           ru,
			points:        unitPoints,
			lineStart:     lineStart,
			lineEnd:       lineEnd,
			seg:           seg,
			segIndex:      segIndex,
			promptVersion: promptVersionExtractRetry,
		}, true
	}

	logFailedUnitRetry(sourceID, seg, u, "retry validation still failed")
	return unitCandidate{}, false
}

func logFailedUnitRetry(sourceID string, seg Segment, u llmUnit, errMsg string) {
	slog.Warn("unit: failed unit retry omitted from unpublished generation",
		"source_id", sourceID,
		"segment", seg.Title,
		"line_start", seg.LineStart,
		"line_end", seg.LineEnd,
		"center", u.Center,
		"error", errMsg,
	)
}

type kpnRelation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type kpnOutput struct {
	Relations []kpnRelation `json:"relations"`
}

// kpnGenerateConcurrencyDefault is used whenever
// cfg.Source.KPNGenerateConcurrency is left at its zero value.
const kpnGenerateConcurrencyDefault = 2

func (s *Service) generateKPN(ctx context.Context, sourceID string) {
	points, err := s.store.GetPointsBySourceID(sourceID)
	if err != nil {
		slog.Error("unit: kpn get points failed", "source_id", sourceID, "error", err)
		return
	}
	if len(points) == 0 {
		return
	}

	units, _ := s.store.GetCompletedUnitsBySourceID(sourceID)
	unitCenterMap := make(map[string]string)
	for _, u := range units {
		unitCenterMap[u.UnitID] = u.Center
	}

	var batches [][]KnowledgePoint
	if len(points) <= 60 {
		batches = [][]KnowledgePoint{points}
	} else {
		outlines, _ := s.sourceStore.GetOutlines(sourceID)
		topLevel := findTopLevelOutlines(outlines)
		grouped := groupPointsByTopOutline(points, units, topLevel)
		for _, batch := range grouped {
			for i := 0; i < len(batch); i += 60 {
				end := i + 60
				if end > len(batch) {
					end = len(batch)
				}
				batches = append(batches, batch[i:end])
			}
		}
	}

	// Batches are independent of each other (each is its own kpn_extract.md
	// call writing relations via InsertRelation, which is INSERT OR IGNORE
	// against a unique index — concurrent inserts across batches are safe),
	// so run them concurrently instead of one after another.
	concurrency := s.cfg.Source.KPNGenerateConcurrency
	if concurrency <= 0 {
		concurrency = kpnGenerateConcurrencyDefault
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, batch := range batches {
		batch := batch
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.kpnBatch(ctx, batch, unitCenterMap)
		}()
	}
	wg.Wait()
}

func findTopLevelOutlines(outlines []source.Outline) []source.Outline {
	var top []source.Outline
	for _, o := range outlines {
		if !o.ParentID.Valid {
			top = append(top, o)
		}
	}
	return top
}

func groupPointsByTopOutline(points []KnowledgePoint, units []KnowledgeUnit, topOutlines []source.Outline) [][]KnowledgePoint {
	unitOutlineMap := make(map[string]string)
	for _, u := range units {
		if u.OutlineID.Valid {
			unitOutlineMap[u.UnitID] = u.OutlineID.String
		}
	}

	groups := make(map[string][]KnowledgePoint)
	ungrouped := "ungrouped"

	for _, p := range points {
		outlineID := unitOutlineMap[p.UnitID]
		if outlineID == "" {
			groups[ungrouped] = append(groups[ungrouped], p)
			continue
		}
		grouped := false
		for _, top := range topOutlines {
			if outlineID == top.OutlineID {
				groups[top.OutlineID] = append(groups[top.OutlineID], p)
				grouped = true
				break
			}
		}
		if !grouped {
			groups[ungrouped] = append(groups[ungrouped], p)
		}
	}

	var result [][]KnowledgePoint
	for _, g := range groups {
		result = append(result, g)
	}
	return result
}

// kpnBatch returns the number of relations actually inserted (new rows, not
// ones InsertRelation silently no-opped as duplicates) — used by
// incrementalKPNForPoint to report how many relations a manual KP add
// produced.
func (s *Service) kpnBatch(ctx context.Context, points []KnowledgePoint, unitCenterMap map[string]string) int {
	var sb strings.Builder
	pointIDs := make(map[string]bool)
	for _, p := range points {
		center := unitCenterMap[p.UnitID]
		sb.WriteString(p.PointID)
		sb.WriteString("\t")
		sb.WriteString(center)
		sb.WriteString("\t")
		sb.WriteString(p.Content)
		sb.WriteString("\n")
		pointIDs[p.PointID] = true
	}

	vars := map[string]string{
		"knowledge_points": sb.String(),
	}

	data, err := s.llmClient.CompleteJSON(ctx, "kpn_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: kpn extraction failed", "error", err)
		return 0
	}

	var output kpnOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: kpn JSON parse failed", "error", err)
		return 0
	}

	created := 0
	for _, rel := range output.Relations {
		if !pointIDs[rel.From] || !pointIDs[rel.To] {
			continue
		}
		if rel.From == rel.To {
			continue
		}

		if rel.Type != "related" && rel.Type != "contradicts" {
			continue
		}

		r := &KnowledgePointRelation{
			SourcePointID: rel.From,
			TargetPointID: rel.To,
			RelationType:  rel.Type,
			Direction:     "bidirectional",
			PromptVersion: promptVersionKPNExtract,
			Scope:         RelationScopeIntra,
		}
		inserted, err := s.store.InsertRelation(r)
		if err != nil {
			slog.Error("unit: insert kpn relation failed", "error", err)
			continue
		}
		if inserted {
			created++
		}
	}
	return created
}

// incrementalKPNForPoint runs KPN relation analysis for one manually-added
// KP (docs/impl/v1/semantics-curation.md "KP 人工修正") against the rest of
// its Source's current KPs, reusing kpnBatch/kpn_extract.md — no new prompt.
// Re-running against points that already have relations is safe: InsertRelation
// is INSERT OR IGNORE against idx_kp_relations_uniq (a global unique index,
// not scoped to intra vs cross), so already-known pairs are silently skipped.
//
// To keep the cost of "add one fact" proportional rather than re-analyzing
// the whole Source every time, large sources are scoped down to newPoint's
// own top-level outline group (the same grouping generateKPN uses to batch
// ≤60 points per LLM call) instead of the full point set.
func (s *Service) incrementalKPNForPoint(ctx context.Context, newPoint KnowledgePoint) (int, error) {
	points, err := s.store.GetPointsBySourceID(newPoint.SourceID)
	if err != nil {
		return 0, fmt.Errorf("unit: incremental kpn: get points: %w", err)
	}

	units, err := s.store.GetCompletedUnitsBySourceID(newPoint.SourceID)
	if err != nil {
		return 0, fmt.Errorf("unit: incremental kpn: get units: %w", err)
	}
	unitCenterMap := make(map[string]string, len(units))
	for _, u := range units {
		unitCenterMap[u.UnitID] = u.Center
	}

	if len(points) <= 60 {
		return s.kpnBatch(ctx, points, unitCenterMap), nil
	}

	outlines, err := s.sourceStore.GetOutlines(newPoint.SourceID)
	if err != nil {
		return 0, fmt.Errorf("unit: incremental kpn: get outlines: %w", err)
	}
	topLevel := findTopLevelOutlines(outlines)
	groups := groupPointsByTopOutline(points, units, topLevel)

	scope := []KnowledgePoint{newPoint}
	for _, g := range groups {
		if containsPointID(g, newPoint.PointID) {
			scope = g
			break
		}
	}

	created := 0
	for i := 0; i < len(scope); i += 60 {
		end := i + 60
		if end > len(scope) {
			end = len(scope)
		}
		batch := scope[i:end]
		if !containsPointID(batch, newPoint.PointID) {
			batch = append(append([]KnowledgePoint{}, batch...), newPoint)
		}
		created += s.kpnBatch(ctx, batch, unitCenterMap)
	}
	return created, nil
}

func containsPointID(points []KnowledgePoint, pointID string) bool {
	for _, p := range points {
		if p.PointID == pointID {
			return true
		}
	}
	return false
}

type conceptMatch struct {
	UnitID  string `json:"unit_id"`
	EntryID string `json:"entry_id"`
}

type conceptMatchOutput struct {
	Matches []conceptMatch `json:"matches"`
}

// unitKindClassification/unitKindClassifyOutput are unit_kind_classify.md's
// output shape (docs/impl/v1/kpn.md 步骤 3, 直接匹配链路 kind-aware 改造
// 2026-08-05): classify-then-branch, reusing the same concept/fact standard
// kpn_entry_propose.md already applies to orphan clustering, so a KU is
// judged the same way regardless of which path (direct match vs orphan)
// it goes through.
type unitKindClassification struct {
	UnitID string `json:"unit_id"`
	Kind   string `json:"kind"`
}

type unitKindClassifyOutput struct {
	Classifications []unitKindClassification `json:"classifications"`
}

// MatchEntries implements source.EntryMatcher: re-runs concept matching
// for sourceID's current KUs against domainID's concept list. Exported so
// source.Service.SetDomain can re-trigger it after a manual domain
// reassignment — matchEntries itself otherwise only runs once, inline,
// during unit_extract. Concept ids are cleared first: matchEntries only ever
// writes a match it found and never clears one that came up empty, so without
// this a KU that doesn't fit any concept in the new domain would keep
// pointing at a concept from its old one.
//
// The Source's existing scope=cross KPN relations are dropped and rebuilt
// (2026-08-08 决策: 修复 entry_id 重分类留下的孤儿关系 — CrossSourceKPN groups
// its matching by entry_id, so every cross relation this Source currently
// has was built against its *old* entry_id grouping; once that grouping is
// cleared and replaced by re-matching, those relations no longer correspond
// to anything and would sit as unexplained noise (e.g. two point_ids linked
// "related" whose current entry_ids don't agree) rather than being
// re-validated. Dropping and letting CrossSourceKPN rebuild from scratch
// under the corrected entry_id is the only way to keep the graph consistent
// with entry_id at all times, not just at initial-extraction time. Intra
// relations are untouched — those never depended on entry_id.
func (s *Service) MatchEntries(ctx context.Context, sourceID, domainID string) {
	// Snapshot the entry_ids this source's current units pointed at before
	// the clear below wipes them — those entries just lost members too and
	// need the same needs_recompile notification new matches get further
	// down (matchConceptBatch), otherwise a rematch that moves a source's
	// units OUT of an entry never flags that entry's page stale.
	staleEntryIDs := s.currentEntryIDsBySourceID(sourceID)

	if err := s.store.ClearEntryIDBySourceID(sourceID); err != nil {
		slog.Warn("unit: clear concept id before rematch failed", "source_id", sourceID, "error", err)
	}
	s.notifyWikiEntriesChanged(staleEntryIDs, fmt.Sprintf("source %s 概念重新匹配：原归属概念成员减少", sourceID))
	var did sql.NullString
	if domainID != "" {
		did = sql.NullString{String: domainID, Valid: true}
	}
	s.matchEntries(ctx, sourceID, did)

	deleted, err := s.store.DeleteCrossRelationsBySourceID(sourceID)
	if err != nil {
		slog.Warn("unit: delete stale cross kpn relations after rematch failed", "source_id", sourceID, "error", err)
	} else if deleted > 0 {
		slog.Info("unit: dropped stale cross kpn relations after entry rematch", "source_id", sourceID, "deleted", deleted)
	}
	// 已比对配对记账也要一并清空——否则这些点会在旧 entry_id 分组下被标记
	// "已经问过"，换到新分组后 FilterUnseenOpposite 会误把它们当作已覆盖过
	// 而跳过，导致新分组下永远不会真正重新匹配。
	if _, err := s.store.DeleteCrossPairsSeenBySourceID(sourceID); err != nil {
		slog.Warn("unit: delete stale cross kpn seen-pairs after rematch failed", "source_id", sourceID, "error", err)
	}
	if _, err := s.CrossSourceKPN(ctx, sourceID); err != nil {
		slog.Warn("unit: rebuild cross kpn after entry rematch failed", "source_id", sourceID, "error", err)
	}
}

// currentEntryIDsBySourceID returns the deduplicated, non-empty entry_ids
// that sourceID's current-lifecycle units currently point at — used by
// MatchEntries to snapshot "who's about to lose a member" before the rematch
// clears entry_id wholesale (see notifyWikiEntriesChanged call site above).
func (s *Service) currentEntryIDsBySourceID(sourceID string) []string {
	units, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		slog.Warn("unit: list current units for entry snapshot failed", "source_id", sourceID, "error", err)
		return nil
	}
	seen := make(map[string]bool, len(units))
	var ids []string
	for _, u := range units {
		if !u.EntryID.Valid || u.EntryID.String == "" || seen[u.EntryID.String] {
			continue
		}
		seen[u.EntryID.String] = true
		ids = append(ids, u.EntryID.String)
	}
	return ids
}

func (s *Service) matchEntries(ctx context.Context, sourceID string, domainID sql.NullString) {
	allUnits, err := s.store.GetCompletedUnitsBySourceID(sourceID)
	if err != nil {
		return
	}
	// Only the current generation gets concept matching — after a
	// re-extraction, the source still carries its superseded predecessors.
	var units []KnowledgeUnit
	for _, u := range allUnits {
		if u.Lifecycle == LifecycleCurrent {
			units = append(units, u)
		}
	}
	if len(units) == 0 {
		return
	}

	did := ""
	if domainID.Valid {
		did = domainID.String
	}
	entries, err := s.store.GetEntriesByDomainID(did)
	if err != nil {
		slog.Warn("unit: get entries failed", "error", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	sourceTitle, sourceSummary, err := s.store.GetSourceTitleSummary(sourceID)
	if err != nil {
		slog.Warn("unit: get source title/summary for entry match failed", "source_id", sourceID, "error", err)
		// Still match with empty source context rather than skipping the batch.
	}
	slog.Info("unit: matching entries", "source_id", sourceID, "domain_id", did, "units", len(units), "entries", len(entries))

	var conceptEntries, factEntries []Concept
	for _, c := range entries {
		if c.Kind == "fact" {
			factEntries = append(factEntries, c)
		} else {
			conceptEntries = append(conceptEntries, c)
		}
	}
	conceptList := renderEntryList(conceptEntries)
	factList := renderEntryList(factEntries)

	// Classify-then-branch (docs/impl/v1/kpn.md 步骤 3, 2026-08-05): decide
	// each unit's concept/fact kind first, then only let it match against
	// same-kind entries — otherwise a fact-shaped unit (a specific
	// product/system) keeps getting absorbed into a nearby broad concept
	// entry just because concept entries dominate the domain's preset, and
	// never gets a chance to match/create a fact entry.
	kindByUnit := s.classifyUnitKinds(ctx, units, sourceTitle, sourceSummary)

	var conceptUnits, factUnits []KnowledgeUnit
	for _, u := range units {
		if kindByUnit[u.UnitID] == "fact" {
			factUnits = append(factUnits, u)
		} else {
			// Missing/failed classification defaults to concept, matching
			// entries.kind's column default and ValidateEntryKind's "" case.
			conceptUnits = append(conceptUnits, u)
		}
	}

	s.matchConceptBatches(ctx, conceptUnits, conceptList, sourceTitle, sourceSummary)
	s.matchConceptBatches(ctx, factUnits, factList, sourceTitle, sourceSummary)
}

// renderEntryList formats entries (already filtered to one kind) into
// unit_entry_match.md's entry_list text.
func renderEntryList(entries []Concept) string {
	var list strings.Builder
	for _, c := range entries {
		line := fmt.Sprintf("[%s] %s：%s", c.EntryID, c.Name, c.Description)
		// Surface preset/evolved aliases and boundary (migration 044) — this
		// is exactly the disambiguation signal those fields were authored
		// for, previously dropped before it ever reached the matching
		// prompt (unit_entry_match.md text unchanged, only entry_list's
		// rendering gains these two segments).
		if len(c.Aliases) > 0 {
			line += fmt.Sprintf("（别名：%s）", strings.Join(c.Aliases, "、"))
		}
		if c.Boundary != "" {
			line += fmt.Sprintf("｜边界：%s", c.Boundary)
		}
		list.WriteString(line + "\n")
	}
	return list.String()
}

// buildUnitsListText renders units into unit_entry_match.md /
// unit_kind_classify.md's shared units_list format.
func buildUnitsListText(units []KnowledgeUnit, sourceTitle, sourceSummary string) string {
	var unitsList strings.Builder
	for _, u := range units {
		// Inject source title/summary alongside center so the model can
		// separate product/document entities (esp. fact entries) without a
		// prompt rewrite — format only; unit_entry_match.md text unchanged.
		line := fmt.Sprintf("[%s] %s | 来源标题：%s", u.UnitID, u.Center, sourceTitle)
		if sourceSummary != "" {
			line += fmt.Sprintf(" | 来源摘要：%s", sourceSummary)
		}
		unitsList.WriteString(line + "\n")
	}
	return unitsList.String()
}

// classifyUnitKinds batches units through unit_kind_classify.md and returns
// each matched unit_id's kind ("concept"/"fact"). Units missing from the
// result (LLM failure, hallucinated id) are simply absent from the map —
// callers default those to concept.
func (s *Service) classifyUnitKinds(ctx context.Context, units []KnowledgeUnit, sourceTitle, sourceSummary string) map[string]string {
	result := make(map[string]string, len(units))
	for i := 0; i < len(units); i += 50 {
		end := i + 50
		if end > len(units) {
			end = len(units)
		}
		batch := units[i:end]
		unitIDSet := make(map[string]bool, len(batch))
		for _, u := range batch {
			unitIDSet[u.UnitID] = true
		}

		vars := map[string]string{"units_list": buildUnitsListText(batch, sourceTitle, sourceSummary)}
		data, err := s.llmClient.CompleteJSON(ctx, "unit_kind_classify.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kind classify LLM failed", "error", err)
			continue
		}
		var output unitKindClassifyOutput
		if err := json.Unmarshal(data, &output); err != nil {
			slog.Warn("unit: kind classify JSON parse failed", "error", err)
			continue
		}
		for _, c := range output.Classifications {
			if !unitIDSet[c.UnitID] {
				continue
			}
			if c.Kind != "concept" && c.Kind != "fact" {
				continue
			}
			result[c.UnitID] = c.Kind
		}
	}
	return result
}

// matchConceptBatches chunks units into unit_entry_match.md-sized batches
// and matches them against entryList. No-ops when either side is empty —
// an empty entryList (e.g. the domain has no fact entries yet) leaves every
// unit's entry_id NULL so it falls through to the orphan candidate path
// instead of wasting an LLM call on a list with nothing to match.
func (s *Service) matchConceptBatches(ctx context.Context, units []KnowledgeUnit, entryList, sourceTitle, sourceSummary string) {
	if len(units) == 0 || entryList == "" {
		return
	}
	for i := 0; i < len(units); i += 50 {
		end := i + 50
		if end > len(units) {
			end = len(units)
		}
		s.matchConceptBatch(ctx, units[i:end], entryList, sourceTitle, sourceSummary)
	}
}

func (s *Service) matchConceptBatch(ctx context.Context, units []KnowledgeUnit, conceptList, sourceTitle, sourceSummary string) {
	unitsList := buildUnitsListText(units, sourceTitle, sourceSummary)
	unitIDSet := make(map[string]bool, len(units))
	for _, u := range units {
		unitIDSet[u.UnitID] = true
	}

	vars := map[string]string{
		"units_list": unitsList,
		"entry_list": conceptList,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_entry_match.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: concept match LLM failed", "error", err)
		return
	}

	var output conceptMatchOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: concept match JSON parse failed", "error", err)
		return
	}

	var matchedEntryIDs []string
	for _, m := range output.Matches {
		if !unitIDSet[m.UnitID] {
			continue
		}
		if m.EntryID == "" {
			continue
		}
		if err := s.store.UpdateUnitEntryID(m.UnitID, &m.EntryID); err != nil {
			slog.Warn("unit: update entry_id failed", "unit_id", m.UnitID, "error", err)
			continue
		}
		matchedEntryIDs = append(matchedEntryIDs, m.EntryID)
	}
	// New Core members for these entry_ids — flag any published page
	// compiled from them (docs/impl/v1/wiki.md「重编译标记」新增 entry_id
	// 归属变化场景).
	s.notifyWikiEntriesChanged(matchedEntryIDs, "知识点新归入概念，Core 成员发生变化")
}

func (s *Service) indexUnit(ku *KnowledgeUnit, mdLines []string) {
	if err := s.indexUnitWithError(ku, mdLines); err != nil {
		slog.Error("unit: bleve index unit failed", "unit_id", ku.UnitID, "error", err)
	}
}

func (s *Service) indexUnitWithError(ku *KnowledgeUnit, mdLines []string) error {
	if s.unitsIndex == nil {
		return nil
	}
	content := sliceLines(mdLines, ku.LineStart, ku.LineEnd)
	lifecycle := ku.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleCurrent
	}
	doc := map[string]interface{}{
		"unit_id":    ku.UnitID,
		"source_id":  ku.SourceID,
		"center":     ku.Center,
		"line_start": ku.LineStart,
		"line_end":   ku.LineEnd,
		"content":    content,
		"lifecycle":  lifecycle,
	}
	if err := s.unitsIndex.Index(ku.UnitID, doc); err != nil {
		return fmt.Errorf("unit: bleve index unit %s: %w", ku.UnitID, err)
	}
	return nil
}

func (s *Service) indexPoint(kp *KnowledgePoint) {
	if err := s.indexPointWithError(kp); err != nil {
		slog.Error("unit: bleve index point failed", "point_id", kp.PointID, "error", err)
	}
}

func (s *Service) indexPointWithError(kp *KnowledgePoint) error {
	if s.pointsIndex == nil {
		return nil
	}
	lifecycle := kp.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleCurrent
	}
	doc := map[string]interface{}{
		"point_id":   kp.PointID,
		"unit_id":    kp.UnitID,
		"source_id":  kp.SourceID,
		"content":    kp.Content,
		"point_type": kp.PointType,
		"lifecycle":  lifecycle,
	}
	if err := s.pointsIndex.Index(kp.PointID, doc); err != nil {
		return fmt.Errorf("unit: bleve index point %s: %w", kp.PointID, err)
	}
	return nil
}

// SetUnitLifecycle is the single entry point for changing KU lifecycle state
// (docs/impl/v1/lifecycle.md 步骤 1). It cascades to the units' KPs, keeps
// Bleve in sync, logs the reason, and notifies the Wiki module if wired in.
// Business code must never UPDATE the lifecycle column directly.
func (s *Service) SetUnitLifecycle(unitIDs []string, lifecycle, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	switch lifecycle {
	case LifecycleCurrent, LifecycleSuperseded, LifecycleDeprecated:
	default:
		return fmt.Errorf("unit: invalid lifecycle %q", lifecycle)
	}

	units, err := s.store.GetUnitsByIDs(unitIDs)
	if err != nil {
		return fmt.Errorf("unit: set lifecycle: get units: %w", err)
	}

	if err := s.store.UpdateUnitsLifecycle(unitIDs, lifecycle); err != nil {
		return fmt.Errorf("unit: set lifecycle: update units: %w", err)
	}
	if err := s.store.UpdatePointsLifecycleByUnitIDs(unitIDs, lifecycle); err != nil {
		return fmt.Errorf("unit: set lifecycle: update points: %w", err)
	}

	points, err := s.store.GetPointsByUnitIDs(unitIDs)
	if err != nil {
		slog.Warn("unit: set lifecycle: get points for reindex failed", "error", err)
	}

	s.reindexLifecycle(units, points, lifecycle)

	slog.Info("unit: lifecycle changed", "unit_ids", unitIDs, "lifecycle", lifecycle, "reason", reason)

	pointIDs := make([]string, len(points))
	for i, p := range points {
		pointIDs[i] = p.PointID
	}

	if s.activationNotifier != nil {
		if err := s.activationNotifier.InvalidateCache(); err != nil {
			slog.Warn("unit: activation notify failed", "error", err)
		}
		if err := s.activationNotifier.NotifyPointsLifecycleChanged(pointIDs); err != nil {
			slog.Warn("unit: activation lifecycle notify failed", "error", err)
		}
	}

	// Core 归属传导（docs/impl/v1/wiki.md「重编译标记」a. lifecycle 传导，
	// 2026-08-18 重新接线）：受影响 units 所属的 entry_id 若被某已发布页面
	// 引用为 Core 成员，标记该页面 needs_recompile——只查 wiki_page_entries
	// 精确匹配，不展开 Context/Conflict 一跳关系（见 WikiEntryNotifier 注释）。
	entryIDs := make([]string, 0, len(units))
	seenEntryIDs := make(map[string]bool, len(units))
	for _, u := range units {
		if !u.EntryID.Valid || u.EntryID.String == "" || seenEntryIDs[u.EntryID.String] {
			continue
		}
		seenEntryIDs[u.EntryID.String] = true
		entryIDs = append(entryIDs, u.EntryID.String)
	}
	s.notifyWikiEntriesChanged(entryIDs, reason)

	return nil
}

// SnapshotAndDeprecate implements the Source module's soft-delete lifecycle
// step: before marking every one of a Source's KUs deprecated, it snapshots
// each one's current lifecycle value so a later RestoreLifecycle call can
// reverse the change precisely — resetting only the ones that were current
// at delete time, not ones already superseded by an earlier reupload.
func (s *Service) SnapshotAndDeprecate(unitIDs []string, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if err := s.store.SnapshotLifecycleBeforeDelete(unitIDs); err != nil {
		return fmt.Errorf("unit: snapshot and deprecate: %w", err)
	}
	return s.SetUnitLifecycle(unitIDs, LifecycleDeprecated, reason)
}

// RestoreLifecycle reverses a prior SnapshotAndDeprecate: each unit is set
// back to its snapshotted pre-delete lifecycle value (grouped, since a
// source's units may have held different lifecycle states at delete time),
// then the snapshot is cleared. Units with no snapshot (never soft-deleted)
// are left untouched.
func (s *Service) RestoreLifecycle(unitIDs []string, reason string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	groups, err := s.store.GroupUnitIDsByLifecycleBeforeDelete(unitIDs)
	if err != nil {
		return fmt.Errorf("unit: restore lifecycle: %w", err)
	}
	for lifecycle, ids := range groups {
		if err := s.SetUnitLifecycle(ids, lifecycle, reason); err != nil {
			return fmt.Errorf("unit: restore lifecycle: %w", err)
		}
	}
	if err := s.store.ClearLifecycleBeforeDelete(unitIDs); err != nil {
		return fmt.Errorf("unit: restore lifecycle: clear snapshot: %w", err)
	}
	return nil
}

// reindexLifecycle rewrites the affected units/points into Bleve with their new
// lifecycle value. Bleve documents are replaced wholesale (no partial update),
// so unit content is re-sliced from the owning source's markdown file.
func (s *Service) reindexLifecycle(units []KnowledgeUnit, points []KnowledgePoint, lifecycle string) {
	for i := range units {
		units[i].Lifecycle = lifecycle
	}
	for i := range points {
		points[i].Lifecycle = lifecycle
	}
	s.reindexUnitsAndPoints(units, points)
}

// ReindexSource rewrites a source's current KU/KP Bleve documents from their
// current DB rows. CompleteShadowSwap calls this (via LifecycleSetter) after
// the 换血事务 reparents the shadow's rows onto the target source_id
// (docs/impl/v1/lifecycle.md 步骤 2): the swap only updates SQLite, while the
// Bleve documents written during the shadow's own pipeline still carry the
// shadow source_id — a value Retrieval's source filter can never match once
// the shadow row is deleted. Document IDs are unit_id/point_id, so indexing
// replaces those stale documents in place. Superseded units are skipped so
// their Bleve body (indexed from the pre-swap markdown) is not overwritten
// by the new file.
func (s *Service) ReindexSource(sourceID string) error {
	// Only current KUs need rewrite after a shadow swap: they were indexed
	// under the shadow source_id. Superseded target KUs were already
	// reindexed with the old markdown in SetUnitLifecycle before the file
	// swap; re-slicing them against the new file would pollute Bleve.
	units, err := s.store.GetUnitsBySourceIDFiltered(sourceID, LifecycleCurrent)
	if err != nil {
		return fmt.Errorf("unit: reindex source: get units: %w", err)
	}
	unitIDs := make([]string, len(units))
	for i, u := range units {
		unitIDs[i] = u.UnitID
	}
	points, err := s.store.GetPointsByUnitIDs(unitIDs)
	if err != nil {
		return fmt.Errorf("unit: reindex source: get points: %w", err)
	}
	s.reindexUnitsAndPoints(units, points)
	return nil
}

// reindexUnitsAndPoints writes the given units/points into Bleve with the
// lifecycle values they carry.
func (s *Service) reindexUnitsAndPoints(units []KnowledgeUnit, points []KnowledgePoint) {
	mdCache := make(map[string][]string)
	pointsByUnit := make(map[string][]KnowledgePoint)
	for _, p := range points {
		pointsByUnit[p.UnitID] = append(pointsByUnit[p.UnitID], p)
	}

	for _, ku := range units {
		ku := ku

		mdLines, cached := mdCache[ku.SourceID]
		if !cached {
			src, err := s.sourceStore.GetByID(ku.SourceID)
			if err != nil {
				slog.Warn("unit: reindex: get source failed", "source_id", ku.SourceID, "error", err)
			} else if data, err := os.ReadFile(src.MarkdownPath); err != nil {
				slog.Warn("unit: reindex: read markdown failed", "source_id", ku.SourceID, "error", err)
			} else {
				mdLines = strings.Split(string(data), "\n")
			}
			mdCache[ku.SourceID] = mdLines
		}
		s.indexUnit(&ku, mdLines)

		for _, kp := range pointsByUnit[ku.UnitID] {
			kp := kp
			s.indexPoint(&kp)
		}
	}
}
