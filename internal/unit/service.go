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
	wikiNotifier       WikiNotifier
	activationNotifier ActivationNotifier

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

// WikiNotifier lets the (not yet implemented) Wiki module learn about
// lifecycle changes so it can mark dependent pages needs_recompile
// (docs/impl/v1/lifecycle.md 步骤 4). SetUnitLifecycle no-ops when unset.
type WikiNotifier interface {
	NotifyPointsLifecycleChanged(pointIDs []string) error
}

// ActivationNotifier lets the Activation module's Matcher learn about KP
// lifecycle changes, since its verified-link cache only holds links whose
// target KP is lifecycle=current (docs/impl/v1/activation.md 步骤 2 候选加载).
// SetUnitLifecycle no-ops when unset.
type ActivationNotifier interface {
	InvalidateCache() error
}

func (s *Service) SetWikiNotifier(n WikiNotifier) {
	s.wikiNotifier = n
}

func (s *Service) SetActivationNotifier(n ActivationNotifier) {
	s.activationNotifier = n
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
	if err := s.extractSegmentsPreInsertDedup(ctx, src.Title, sourceID, segments, mdLines, func() {
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
	s.emit(sourceID, progress.Event{Step: progress.StepConceptMatch, Status: progress.StatusStarted, Message: "概念匹配"})
	s.matchConcepts(ctx, sourceID, src.DomainID)
	s.emit(sourceID, progress.Event{Step: progress.StepConceptMatch, Status: progress.StatusCompleted, ElapsedMs: time.Since(stepStart).Milliseconds()})

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
	promptVersionKPNCross     = "v1" // kpn_cross_match.md
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

	if len(points) <= 60 {
		s.kpnBatch(ctx, points, unitCenterMap)
	} else {
		outlines, _ := s.sourceStore.GetOutlines(sourceID)
		topLevel := findTopLevelOutlines(outlines)
		batches := groupPointsByTopOutline(points, units, topLevel)
		for _, batch := range batches {
			for i := 0; i < len(batch); i += 60 {
				end := i + 60
				if end > len(batch) {
					end = len(batch)
				}
				s.kpnBatch(ctx, batch[i:end], unitCenterMap)
			}
		}
	}
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

func (s *Service) kpnBatch(ctx context.Context, points []KnowledgePoint, unitCenterMap map[string]string) {
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
		return
	}

	var output kpnOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: kpn JSON parse failed", "error", err)
		return
	}

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
		if _, err := s.store.InsertRelation(r); err != nil {
			slog.Error("unit: insert kpn relation failed", "error", err)
		}
	}
}

type conceptMatch struct {
	UnitID    string `json:"unit_id"`
	ConceptID string `json:"concept_id"`
}

type conceptMatchOutput struct {
	Matches []conceptMatch `json:"matches"`
}

func (s *Service) matchConcepts(ctx context.Context, sourceID string, domainID sql.NullString) {
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
	concepts, err := s.store.GetConceptsByDomainID(did)
	if err != nil {
		slog.Warn("unit: get concepts failed", "error", err)
		return
	}
	if len(concepts) == 0 {
		return
	}

	var conceptList strings.Builder
	for _, c := range concepts {
		conceptList.WriteString(fmt.Sprintf("[%s] %s：%s\n", c.ConceptID, c.Name, c.Description))
	}

	for i := 0; i < len(units); i += 50 {
		end := i + 50
		if end > len(units) {
			end = len(units)
		}
		s.matchConceptBatch(ctx, units[i:end], conceptList.String())
	}
}

func (s *Service) matchConceptBatch(ctx context.Context, units []KnowledgeUnit, conceptList string) {
	var unitsList strings.Builder
	unitIDSet := make(map[string]bool)
	for _, u := range units {
		unitsList.WriteString(fmt.Sprintf("[%s] %s\n", u.UnitID, u.Center))
		unitIDSet[u.UnitID] = true
	}

	vars := map[string]string{
		"units_list":   unitsList.String(),
		"concept_list": conceptList,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_concept_match.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: concept match LLM failed", "error", err)
		return
	}

	var output conceptMatchOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: concept match JSON parse failed", "error", err)
		return
	}

	for _, m := range output.Matches {
		if !unitIDSet[m.UnitID] {
			continue
		}
		if m.ConceptID == "" {
			continue
		}
		if err := s.store.UpdateUnitConceptID(m.UnitID, &m.ConceptID); err != nil {
			slog.Warn("unit: update concept_id failed", "unit_id", m.UnitID, "error", err)
		}
	}
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

	if s.wikiNotifier != nil {
		pointIDs := make([]string, len(points))
		for i, p := range points {
			pointIDs[i] = p.PointID
		}
		if err := s.wikiNotifier.NotifyPointsLifecycleChanged(pointIDs); err != nil {
			slog.Warn("unit: wiki notify failed", "error", err)
		}
	}

	if s.activationNotifier != nil {
		if err := s.activationNotifier.InvalidateCache(); err != nil {
			slog.Warn("unit: activation notify failed", "error", err)
		}
	}

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

// ReindexSource rewrites every one of a source's KU/KP Bleve documents from
// their current DB rows. CompleteShadowSwap calls this (via LifecycleSetter)
// after the 换血事务 reparents the shadow's rows onto the target source_id
// (docs/impl/v1/lifecycle.md 步骤 2): the swap only updates SQLite, while the
// Bleve documents written during the shadow's own pipeline still carry the
// shadow source_id — a value Retrieval's source filter can never match once
// the shadow row is deleted. Document IDs are unit_id/point_id, so indexing
// replaces those stale documents in place.
func (s *Service) ReindexSource(sourceID string) error {
	units, err := s.store.GetUnitsBySourceID(sourceID)
	if err != nil {
		return fmt.Errorf("unit: reindex source: get units: %w", err)
	}
	unitIDs := make([]string, len(units))
	for i, u := range units {
		unitIDs[i] = u.UnitID
	}
	// GetPointsByUnitIDs rather than GetPointsBySourceID: the latter filters
	// to current-lifecycle rows, but every document must be rewritten here —
	// superseded ones included — since they all still carry the stale source_id.
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
