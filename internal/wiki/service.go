package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

var requiredSections = []string{"## 稳定结论", "## 展开说明", "## 待验证点", "## 依赖来源"}

var pointIDTagRe = regexp.MustCompile(`\[([^\[\]\s]+)\]`)

type Service struct {
	store                  *Store
	llmClient              llm.LLMClient
	wikiIndex              bleve.Index
	cfg                    config.WikiConfig
	qualifyingMinConfident int
	activationSvc          *activation.Service
}

// qualifyingMinConfident should be study.StudyConfig.WikiConfidentMin — Wiki
// doesn't import the study package (docs/impl/v1/wiki.md 步骤 3, "与 Study
// 候选口径一致" is a data-contract statement, not a Go dependency), so the
// caller (main.go) threads the same config.yml value through here.
func NewService(store *Store, llmClient llm.LLMClient, wikiIndex bleve.Index, cfg config.WikiConfig, qualifyingMinConfident int) *Service {
	return &Service{store: store, llmClient: llmClient, wikiIndex: wikiIndex, cfg: cfg, qualifyingMinConfident: qualifyingMinConfident}
}

// SetActivationSvc wires the (optional) dependency Compile needs to resolve
// the pending_confirm wiki_candidate learning_result (docs/impl/v1/wiki.md
// 步骤 2). Compile still works without it (result_id resolution just no-ops).
func (s *Service) SetActivationSvc(a *activation.Service) {
	s.activationSvc = a
}

// Compile implements docs/impl/v1/wiki.md 步骤 2-3: POST /wiki/compile.
func (s *Service) Compile(ctx context.Context, req CompileRequest) (*Page, error) {
	if req.PageType != PageTypeTopic && req.PageType != PageTypeConcept {
		return nil, fmt.Errorf("wiki: invalid page_type %q", req.PageType)
	}

	if req.ResultID != "" {
		if s.activationSvc != nil {
			if err := s.activationSvc.Store().ResolvePending(req.ResultID, activation.ResultApplied, "manual"); err != nil {
				slog.Warn("wiki: resolve pending wiki_candidate result failed", "result_id", req.ResultID, "error", err)
			}
		}
	} else {
		slog.Warn("wiki: compile triggered without result_id (debug path)", "concept_id", req.ConceptID)
	}

	if existing, err := s.store.GetActivePageByConceptID(req.ConceptID); err != nil {
		return nil, fmt.Errorf("wiki: check existing page: %w", err)
	} else if existing != nil {
		return nil, ErrPageAlreadyExists
	}

	compiled, err := s.compileContent(ctx, req.ConceptID, req.PageType)
	if err != nil {
		return nil, err
	}

	page := &Page{
		PageType:         req.PageType,
		Title:            compiled.title,
		Content:          compiled.content,
		Status:           StatusDraft,
		SourcePointIDs:   marshalIDs(compiled.sourcePointIDs),
		SourceUnitIDs:    marshalIDs(compiled.sourceUnitIDs),
		SourceLinkIDs:    marshalIDs(compiled.sourceLinkIDs),
		Aliases:          marshalIDs(compiled.aliases),
		TriggerQuestions: marshalIDs(compiled.triggerQuestions),
		CompiledFrom:     marshalIDs(nonEmpty(req.ResultID)),
		PromptVersion:    "v1",
		ModelName:        "reasoning",
	}
	page.ConceptID = nullableString(req.ConceptID)

	if err := s.store.InsertPage(page); err != nil {
		return nil, err
	}
	if err := s.store.InsertRevision(&Revision{PageID: page.PageID, Content: page.Content, Reason: "compile"}); err != nil {
		slog.Error("wiki: insert initial revision failed", "page_id", page.PageID, "error", err)
	}

	slog.Info("wiki: compiled draft page", "page_id", page.PageID, "concept_id", req.ConceptID,
		"source_point_ids", len(compiled.sourcePointIDs))
	return s.store.GetPage(page.PageID)
}

// Recompile implements docs/impl/v1/wiki.md 步骤 5: re-run compilation for an
// existing (non-archived) page, writing a new revision and resetting it to
// draft — the caller must publish again.
func (s *Service) Recompile(ctx context.Context, pageID, reason string, compiledFrom []string) (*Page, error) {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	if page.Status == StatusArchived {
		return nil, ErrPageArchived
	}

	conceptID := ""
	if page.ConceptID.Valid {
		conceptID = page.ConceptID.String
	}
	compiled, err := s.compileContent(ctx, conceptID, page.PageType)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(pageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalIDs(compiledFrom), "v1", "reasoning"); err != nil {
		return nil, err
	}
	if err := s.store.InsertRevision(&Revision{PageID: pageID, Content: compiled.content, Reason: reason}); err != nil {
		slog.Error("wiki: insert recompile revision failed", "page_id", pageID, "error", err)
	}

	// A page must not answer directly while it's being recompiled/awaiting
	// re-publish under a stale index entry.
	if err := s.wikiIndex.Delete(pageID); err != nil {
		slog.Warn("wiki: remove page from index after recompile failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: recompiled page", "page_id", pageID, "reason", reason)
	return s.store.GetPage(pageID)
}

type compiledContent struct {
	title            string
	content          string
	sourcePointIDs   []string
	sourceUnitIDs    []string
	sourceLinkIDs    []string
	aliases          []string
	triggerQuestions []string
}

// compileContent implements docs/impl/v1/wiki.md 步骤 3: gather inputs, call
// the LLM once (retrying once on validation failure), and validate the
// result. Shared by Compile and Recompile.
func (s *Service) compileContent(ctx context.Context, conceptID, pageType string) (*compiledContent, error) {
	conceptName, conceptDesc, _, err := s.store.GetConceptInfo(conceptID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get concept info: %w", err)
	}

	qualifying, err := s.store.ListQualifyingPoints(conceptID, s.qualifyingMinConfident)
	if err != nil {
		return nil, fmt.Errorf("wiki: list qualifying points: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, fmt.Errorf("wiki: no qualifying points for concept %s", conceptID)
	}

	maxChars := s.cfg.CompileMaxChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	materials, usedPointIDs, _ := s.gatherMaterials(qualifying, maxChars)

	gapsText, err := s.matchingGaps(conceptName, qualifying)
	if err != nil {
		slog.Warn("wiki: gather gaps failed, continuing without them", "concept_id", conceptID, "error", err)
	}

	pageTypeHint := "本页面类型为 concept（概念页）：标题使用概念名称本身。"
	if pageType == PageTypeTopic {
		pageTypeHint = "本页面类型为 topic（主题页）：标题请根据材料内容概括一个合适的主题名称，不必是概念名称本身。"
	}

	whitelist := make(map[string]bool, len(usedPointIDs))
	for _, id := range usedPointIDs {
		whitelist[id] = true
	}

	vars := map[string]string{
		"concept_name":        conceptName,
		"concept_description": conceptDesc,
		"materials":           materials,
		"gaps":                gapsText,
		"page_type_hint":      pageTypeHint,
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_compile.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: compile llm call failed", "attempt", attempt, "concept_id", conceptID, "error", err)
			continue
		}

		var output struct {
			Title            string   `json:"title"`
			Content          string   `json:"content"`
			CitedPointIDs    []string `json:"cited_point_ids"`
			Aliases          []string `json:"aliases"`
			TriggerQuestions []string `json:"trigger_questions"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: compile parse failed", "attempt", attempt, "concept_id", conceptID, "error", err)
			continue
		}

		// cited_point_ids ⊆ whitelist (out-of-bounds ids dropped, warn).
		var droppedCited []string
		for _, id := range output.CitedPointIDs {
			if !whitelist[id] {
				droppedCited = append(droppedCited, id)
			}
		}
		if len(droppedCited) > 0 {
			slog.Warn("wiki: dropped out-of-whitelist cited_point_ids", "concept_id", conceptID, "ids", droppedCited)
		}

		// [point_id] tags in content: strip any not in the whitelist.
		filteredContent, citedInContent, stripped := filterContentTags(output.Content, whitelist)
		if len(stripped) > 0 {
			slog.Warn("wiki: stripped out-of-whitelist point_id tags from content", "concept_id", conceptID, "ids", stripped)
		}

		if !hasRequiredSections(filteredContent) {
			lastErr = fmt.Errorf("wiki: compiled content missing required sections")
			slog.Warn("wiki: compile missing required sections", "attempt", attempt, "concept_id", conceptID)
			continue
		}
		if strings.TrimSpace(output.Title) == "" {
			lastErr = fmt.Errorf("wiki: compiled title is empty")
			slog.Warn("wiki: compile empty title", "attempt", attempt, "concept_id", conceptID)
			continue
		}

		sourceUnitIDs := sourceUnitsForPoints(citedInContent, qualifying)
		sourceLinkIDs, err := s.store.VerifiedLinkIDsForPoints(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup verified link ids for cited points failed", "concept_id", conceptID, "error", err)
		}

		triggerMax := s.cfg.TriggerQuestionsMax
		if triggerMax <= 0 {
			triggerMax = 10
		}
		if len(output.Aliases) == 0 || len(output.TriggerQuestions) == 0 {
			slog.Warn("wiki: compile output missing aliases/trigger_questions, storing empty",
				"concept_id", conceptID, "aliases", len(output.Aliases), "trigger_questions", len(output.TriggerQuestions))
		}

		return &compiledContent{
			title:            output.Title,
			content:          filteredContent,
			sourcePointIDs:   citedInContent,
			sourceUnitIDs:    sourceUnitIDs,
			sourceLinkIDs:    sourceLinkIDs,
			aliases:          truncateStrings(output.Aliases, triggerMax),
			triggerQuestions: truncateStrings(output.TriggerQuestions, triggerMax),
		}, nil
	}

	return nil, fmt.Errorf("wiki: compile failed after retry: %w", lastErr)
}

// gatherMaterials implements docs/impl/v1/wiki.md 步骤 3's KU text budget:
// group qualifying points by KU (avoiding re-reading a KU's text twice),
// stopping once compile_max_chars is reached — points are already ordered by
// confident_count descending, so this is exactly "超出按 confident_count 降序截取".
func (s *Service) gatherMaterials(points []QualifyingPoint, maxChars int) (materialsText string, usedPointIDs, usedUnitIDs []string) {
	type unitEntry struct {
		center   string
		content  string
		pointIDs []string
	}
	entries := make(map[string]*unitEntry)
	var order []string
	totalChars := 0

	for _, p := range points {
		e, exists := entries[p.UnitID]
		if !exists {
			content, err := s.readUnitContent(p.SourceID, p.LineStart, p.LineEnd)
			if err != nil {
				slog.Warn("wiki: read unit content failed, skipping", "unit_id", p.UnitID, "error", err)
				continue
			}
			n := len([]rune(content))
			if totalChars+n > maxChars && len(order) > 0 {
				break
			}
			e = &unitEntry{center: p.UnitCenter, content: content}
			entries[p.UnitID] = e
			order = append(order, p.UnitID)
			totalChars += n
			usedUnitIDs = append(usedUnitIDs, p.UnitID)
		}
		e.pointIDs = append(e.pointIDs, p.PointID)
		usedPointIDs = append(usedPointIDs, p.PointID)
	}

	var sb strings.Builder
	for _, uid := range order {
		e := entries[uid]
		fmt.Fprintf(&sb, "【知识点：%s】主题：%s\n原文：\n%s\n\n", strings.Join(e.pointIDs, ", "), e.center, e.content)
	}
	return sb.String(), usedPointIDs, usedUnitIDs
}

// matchingGaps implements docs/impl/v1/wiki.md 步骤 3's gap material: reuses
// the shared foundation/text tokenizer to find knowledge_gaps whose
// question_terms overlap with the concept name + qualifying KP content.
func (s *Service) matchingGaps(conceptName string, points []QualifyingPoint) (string, error) {
	gaps, err := s.store.TopKnowledgeGaps(50)
	if err != nil {
		return "", err
	}
	if len(gaps) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString(conceptName)
	for _, p := range points {
		sb.WriteString(" ")
		sb.WriteString(p.Content)
	}
	conceptTerms := text.TermSet(sb.String())

	var matched []string
	for _, g := range gaps {
		gapTerms := text.SplitTerms(g.QuestionTerms)
		overlap := false
		for t := range gapTerms {
			if _, ok := conceptTerms[t]; ok {
				overlap = true
				break
			}
		}
		if !overlap {
			continue
		}
		matched = append(matched, fmt.Sprintf("- %s（命中 %d 次）", g.Question, g.HitCount))
		if len(matched) >= 10 {
			break
		}
	}
	return strings.Join(matched, "\n"), nil
}

func sourceUnitsForPoints(pointIDs []string, points []QualifyingPoint) []string {
	unitByPoint := make(map[string]string, len(points))
	for _, p := range points {
		unitByPoint[p.PointID] = p.UnitID
	}
	seen := make(map[string]bool)
	var units []string
	for _, pid := range pointIDs {
		uid, ok := unitByPoint[pid]
		if !ok || seen[uid] {
			continue
		}
		seen[uid] = true
		units = append(units, uid)
	}
	return units
}

func hasRequiredSections(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	for _, h := range requiredSections {
		if !strings.Contains(content, h) {
			return false
		}
	}
	return true
}

// filterContentTags strips any [point_id] tag not in whitelist
// (docs/impl/v1/wiki.md 步骤 3, 编译后校验), returning the filtered content,
// the deduped set of whitelisted ids actually cited, and the stripped ids.
func filterContentTags(content string, whitelist map[string]bool) (filtered string, cited, stripped []string) {
	seen := make(map[string]bool)
	filtered = pointIDTagRe.ReplaceAllStringFunc(content, func(tag string) string {
		id := tag[1 : len(tag)-1]
		if whitelist[id] {
			if !seen[id] {
				seen[id] = true
				cited = append(cited, id)
			}
			return tag
		}
		stripped = append(stripped, id)
		return ""
	})
	return filtered, cited, stripped
}

// Publish implements docs/impl/v1/wiki.md 步骤 4: POST /wiki/pages/:id/publish.
// Only valid from draft or needs_recompile.
func (s *Service) Publish(pageID string) (*Page, error) {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	if page.Status != StatusDraft && page.Status != StatusNeedsRecompile {
		return nil, fmt.Errorf("%w: publish only valid from draft/needs_recompile, page %s is %s", ErrInvalidStateTransition, pageID, page.Status)
	}

	if err := s.store.PublishPage(pageID); err != nil {
		return nil, err
	}
	if err := s.indexPage(page); err != nil {
		slog.Error("wiki: index page after publish failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: published page", "page_id", pageID, "concept_id", page.ConceptID.String)
	return s.store.GetPage(pageID)
}

// Archive implements docs/impl/v1/wiki.md 步骤 6: POST /wiki/pages/:id/archive.
// Terminal — an archived page can't be recompiled or published again.
func (s *Service) Archive(pageID string) (*Page, error) {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}

	if err := s.store.UpdatePageStatus(pageID, StatusArchived); err != nil {
		return nil, err
	}
	if err := s.wikiIndex.Delete(pageID); err != nil {
		slog.Warn("wiki: remove archived page from index failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: archived page", "page_id", pageID)
	return s.store.GetPage(pageID)
}

// MarkNeedsRecompile implements docs/impl/v1/wiki.md 步骤 5: pulls a page out
// of the index immediately ("宁可回落慢路径也不用可疑页面直答") and flips it
// to needs_recompile. A no-op for pages that are already archived (terminal)
// or already needs_recompile.
func (s *Service) MarkNeedsRecompile(pageID, reason string) error {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return err
	}
	if page == nil || page.Status == StatusArchived || page.Status == StatusNeedsRecompile {
		return nil
	}

	if err := s.store.UpdatePageStatus(pageID, StatusNeedsRecompile); err != nil {
		return err
	}
	if err := s.wikiIndex.Delete(pageID); err != nil {
		slog.Warn("wiki: remove page from index for recompile failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: marked page needs_recompile", "page_id", pageID, "reason", reason)
	return nil
}

// GetActivePageByConceptID exposes the store lookup for callers outside this
// package (docs/impl/v1/concept-evolution.md 步骤 3 merge 执行: find the page
// tied to a concept being merged so it can be flagged needs_recompile).
func (s *Service) GetActivePageByConceptID(conceptID string) (*Page, error) {
	return s.store.GetActivePageByConceptID(conceptID)
}

// RecompileFlag is one page ScanForNewQualifyingKP marked needs_recompile,
// returned so the caller (Study) can write the recompile_flag audit trail
// (docs/impl/v1/study.md 步骤 6, docs/impl/v1/wiki.md 步骤 5b).
type RecompileFlag struct {
	PageID    string
	ConceptID string
	Reason    string
}

// ScanForNewQualifyingKP implements docs/impl/v1/wiki.md 步骤 5b: for every
// published page tied to a concept, compare currentQualifyingCounts (Study's
// fresh count of KPs meeting wiki.wiki_confident_min for that concept, same
// query semantics as this package's own qualifying-KP query) against the
// count actually compiled into the page — approximated by len(source_point_ids)
// since the compile-time qualifying count itself isn't a persisted column.
// A difference >= minNewKP marks the page needs_recompile and reports it.
func (s *Service) ScanForNewQualifyingKP(currentQualifyingCounts map[string]int, minNewKP int) ([]RecompileFlag, error) {
	pages, err := s.store.ListPublishedPages()
	if err != nil {
		return nil, fmt.Errorf("wiki: list published pages: %w", err)
	}

	var flagged []RecompileFlag
	for _, p := range pages {
		if !p.ConceptID.Valid {
			continue
		}
		current, ok := currentQualifyingCounts[p.ConceptID.String]
		if !ok {
			continue
		}
		var sourcePointIDs []string
		json.Unmarshal([]byte(p.SourcePointIDs), &sourcePointIDs)
		if current-len(sourcePointIDs) < minNewKP {
			continue
		}
		reason := fmt.Sprintf("概念新增合格知识点 %d 个（当前 %d，编译时 %d）", current-len(sourcePointIDs), current, len(sourcePointIDs))
		if err := s.MarkNeedsRecompile(p.PageID, reason); err != nil {
			slog.Error("wiki: mark needs_recompile from new qualifying kp failed", "page_id", p.PageID, "error", err)
			continue
		}
		flagged = append(flagged, RecompileFlag{PageID: p.PageID, ConceptID: p.ConceptID.String, Reason: reason})
	}
	return flagged, nil
}

// NotifyPointsLifecycleChanged implements unit.ActivationNotifier's sibling
// interface, unit.WikiNotifier (docs/impl/v1/lifecycle.md 步骤 4): any
// published page whose source_point_ids intersects pointIDs is marked
// needs_recompile — a superseded/deprecated KP invalidates the page's
// "stable conclusions" until it's re-verified.
func (s *Service) NotifyPointsLifecycleChanged(pointIDs []string) error {
	if len(pointIDs) == 0 {
		return nil
	}
	changed := make(map[string]bool, len(pointIDs))
	for _, id := range pointIDs {
		changed[id] = true
	}

	pages, err := s.store.ListPublishedPages()
	if err != nil {
		return fmt.Errorf("wiki: list published pages: %w", err)
	}

	for _, p := range pages {
		var sourcePointIDs []string
		if err := json.Unmarshal([]byte(p.SourcePointIDs), &sourcePointIDs); err != nil {
			continue
		}
		affected := false
		for _, pid := range sourcePointIDs {
			if changed[pid] {
				affected = true
				break
			}
		}
		if !affected {
			continue
		}
		if err := s.MarkNeedsRecompile(p.PageID, "lifecycle_changed"); err != nil {
			slog.Error("wiki: mark needs_recompile from lifecycle change failed", "page_id", p.PageID, "error", err)
		}
	}
	return nil
}

// TryDirectAnswer implements docs/impl/v1/wiki.md 步骤 4 (retrieval.md 第 0
// 层's callee): gather up to maxCandidates direct-answer candidates from two
// entries — lexical (wiki index, score >= minScore) and concept (question
// text contains a published page's concept name) — and try them in order,
// stopping at the first page that reports sufficient=true. minScore and
// maxCandidates are retrieval.RetrievalConfig.WikiMinScore/WikiMaxCandidates,
// passed in by the caller since Wiki doesn't depend on the retrieval
// package's config section. maxCandidates<=0 defaults to 3.
func (s *Service) TryDirectAnswer(ctx context.Context, question string, minScore float64, maxCandidates int) (*DirectAnswerResult, bool, error) {
	if maxCandidates <= 0 {
		maxCandidates = 3
	}

	candidates, err := s.gatherDirectAnswerCandidates(question, minScore, maxCandidates)
	if err != nil {
		return nil, false, err
	}

	for _, pageID := range candidates {
		page, err := s.store.GetPage(pageID)
		if err != nil {
			return nil, false, fmt.Errorf("wiki: get page: %w", err)
		}
		if page == nil || page.Status != StatusPublished {
			// Index/DB momentarily out of sync (e.g. mid recompile) — skip.
			continue
		}

		result, ok, err := s.answerFromPage(ctx, question, page)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return result, true, nil
		}
	}
	return nil, false, nil
}

// gatherDirectAnswerCandidates implements docs/impl/v1/wiki.md 步骤 4's two
// direct-answer entries, merged and deduped: lexical hits (wiki index,
// including aliases/trigger_questions fields, score >= minScore) ordered
// first, then concept-name hits not already present, truncated to
// maxCandidates. Neither entry calls the LLM.
func (s *Service) gatherDirectAnswerCandidates(question string, minScore float64, maxCandidates int) ([]string, error) {
	q := bleve.NewMatchQuery(question)
	req := bleve.NewSearchRequest(q)
	req.Size = maxCandidates

	results, err := s.wikiIndex.Search(req)
	if err != nil {
		return nil, fmt.Errorf("wiki: search: %w", err)
	}

	seen := make(map[string]bool)
	var candidates []string
	for _, hit := range results.Hits {
		if hit.Score < minScore || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		candidates = append(candidates, hit.ID)
	}

	conceptHits, err := s.matchConceptEntry(question)
	if err != nil {
		slog.Warn("wiki: concept entry lookup failed, continuing with lexical candidates only", "error", err)
	}
	for _, pageID := range conceptHits {
		if seen[pageID] {
			continue
		}
		seen[pageID] = true
		candidates = append(candidates, pageID)
	}

	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	return candidates, nil
}

// matchConceptEntry implements docs/impl/v1/wiki.md 步骤 4's concept入口:
// word-lexical containment (not embedding, not LLM) between the question and
// every published page's concept name, so a question mentioning the concept
// but not the page's wording (or the wiki index's aliases/trigger_questions)
// still finds the page.
func (s *Service) matchConceptEntry(question string) ([]string, error) {
	pages, err := s.store.ListPublishedConceptPages()
	if err != nil {
		return nil, fmt.Errorf("wiki: list published concept pages: %w", err)
	}
	if len(pages) == 0 {
		return nil, nil
	}

	qCompact := text.NormalizeCompact(question)
	var pageIDs []string
	for _, p := range pages {
		nameCompact := text.NormalizeCompact(p.Name)
		if nameCompact == "" || !strings.Contains(qCompact, nameCompact) {
			continue
		}
		pageIDs = append(pageIDs, p.PageID)
	}
	return pageIDs, nil
}

// answerFromPage implements docs/impl/v1/wiki.md 步骤 4's per-candidate direct
// answer: ask the page whether it can answer the question, and if so,
// citation-whitelist the result against the page's source_point_ids.
func (s *Service) answerFromPage(ctx context.Context, question string, page *Page) (*DirectAnswerResult, bool, error) {
	vars := map[string]string{
		"question": question,
		"title":    page.Title,
		"content":  page.Content,
	}
	raw, err := s.llmClient.CompleteJSON(ctx, "answer_wiki.md", vars, "default")
	if err != nil {
		return nil, false, fmt.Errorf("wiki: answer llm call: %w", err)
	}

	var output struct {
		Content    string   `json:"content"`
		Citations  []string `json:"citations"`
		Sufficient bool     `json:"sufficient"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, false, fmt.Errorf("wiki: answer parse: %w", err)
	}
	if !output.Sufficient {
		return nil, false, nil
	}

	var sourcePointIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	whitelist := make(map[string]bool, len(sourcePointIDs))
	for _, id := range sourcePointIDs {
		whitelist[id] = true
	}

	var cited, hallucinated []string
	for _, c := range output.Citations {
		if whitelist[c] {
			cited = append(cited, c)
		} else {
			hallucinated = append(hallucinated, c)
		}
	}
	if len(hallucinated) > 0 {
		slog.Warn("wiki: filtered hallucinated citations from direct answer", "page_id", page.PageID, "hallucinated", hallucinated)
	}

	return &DirectAnswerResult{PageID: page.PageID, Content: output.Content, CitedPointIDs: cited}, true, nil
}

// indexPage writes the fields retrieval.md 第 0 层's search relies on — only
// called for status=published pages (docs/impl/v1/wiki.md 数据结构, "只索引
// status=published 的页面").
func (s *Service) indexPage(page *Page) error {
	var aliases, triggerQuestions []string
	json.Unmarshal([]byte(page.Aliases), &aliases)
	json.Unmarshal([]byte(page.TriggerQuestions), &triggerQuestions)

	doc := map[string]interface{}{
		"page_id":           page.PageID,
		"title":             page.Title,
		"content":           page.Content,
		"aliases":           strings.Join(aliases, " "),
		"trigger_questions": strings.Join(triggerQuestions, " "),
		"concept_id":        page.ConceptID.String,
		"status":            StatusPublished,
	}
	return s.wikiIndex.Index(page.PageID, doc)
}

func (s *Service) readUnitContent(sourceID string, lineStart, lineEnd int) (string, error) {
	mdPath, err := s.store.GetSourceMarkdownPath(sourceID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("wiki: read markdown %s: %w", mdPath, err)
	}
	lines := strings.Split(string(data), "\n")
	start, end := lineStart, lineEnd
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return "", nil
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}

// truncateStrings implements wiki.trigger_questions_max ("超出条数上限截断保
// 留前 N 条", docs/impl/v1/wiki.md 步骤 3) — applied identically to aliases
// and trigger_questions.
func truncateStrings(items []string, max int) []string {
	if len(items) <= max {
		return items
	}
	return items[:max]
}

func marshalIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
