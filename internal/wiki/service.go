package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

var requiredSections = []string{"## 稳定结论", "## 展开说明", "## 待验证点", "## 依赖来源"}

var pointIDTagRe = regexp.MustCompile(`\[([^\[\]\s]+)\]`)

type Service struct {
	store         *Store
	llmClient     llm.LLMClient
	wikiIndex     bleve.Index
	cfg           config.WikiConfig
	activationSvc *activation.Service
}

func NewService(store *Store, llmClient llm.LLMClient, wikiIndex bleve.Index, cfg config.WikiConfig) *Service {
	return &Service{store: store, llmClient: llmClient, wikiIndex: wikiIndex, cfg: cfg}
}

// SetActivationSvc wires the (optional) dependency Compile needs to resolve
// the pending_confirm wiki_candidate learning_result (docs/impl/v1/wiki.md
// 步骤 2). Compile still works without it (result_id resolution just no-ops).
func (s *Service) SetActivationSvc(a *activation.Service) {
	s.activationSvc = a
}

// Analyze implements docs/impl/v1/wiki.md 步骤 2: POST /wiki/compile/analyze.
// Read-only — it changes no state (does not resolve any pending_confirm
// wiki_candidate) and its result is never persisted. The caller is expected
// to show it to a human and, on confirmation, send it back as
// CompileRequest.Claims/Tensions (docs/design/wiki-compilation.md "编译内部
// 分两步").
func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResult, error) {
	// page_type=topic is exclusively a second-tier compile output now
	// (docs/impl/v1/wiki.md 步骤 8, "数据结构" 两层架构扩展): topic pages are
	// produced only by POST /wiki/pages/:id/topic/analyze|compile, which
	// takes already-published concept pages as material, not KPs. This
	// KP-based one-tier path only ever produces concept pages.
	if req.PageType != PageTypeConcept {
		return nil, fmt.Errorf("wiki: invalid page_type %q (topic pages are compiled via POST /wiki/pages/:id/topic/analyze|compile)", req.PageType)
	}

	claims, tensions, err := s.analyzeClaims(ctx, req.ConceptID)
	if err != nil {
		return nil, err
	}
	return &AnalyzeResult{
		ConceptID: req.ConceptID,
		PageType:  req.PageType,
		ResultID:  req.ResultID,
		Claims:    claims,
		Tensions:  tensions,
	}, nil
}

// Compile implements docs/impl/v1/wiki.md 步骤 2-3: POST /wiki/compile.
func (s *Service) Compile(ctx context.Context, req CompileRequest) (*Page, error) {
	if req.PageType != PageTypeConcept {
		return nil, fmt.Errorf("wiki: invalid page_type %q (topic pages are compiled via POST /wiki/pages/:id/topic/analyze|compile)", req.PageType)
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

	claims, tensions := req.Claims, req.Tensions
	if len(claims) == 0 {
		// No analysis round-tripped back (debug path, or caller skipped
		// /wiki/compile/analyze) — run it internally so generation is still
		// constrained to an analysis result, not raw material access.
		var err error
		claims, tensions, err = s.analyzeClaims(ctx, req.ConceptID)
		if err != nil {
			return nil, err
		}
	}

	compiled, err := s.compileContent(ctx, req.ConceptID, req.PageType, claims, tensions)
	if err != nil {
		return nil, err
	}

	page := &Page{
		PageType:           req.PageType,
		Title:              compiled.title,
		Content:            compiled.content,
		Status:             StatusDraft,
		SourcePointIDs:     marshalIDs(compiled.sourcePointIDs),
		SourceUnitIDs:      marshalIDs(compiled.sourceUnitIDs),
		SourceLinkIDs:      marshalIDs(compiled.sourceLinkIDs),
		ObservedConditions: marshalConditions(compiled.observedConditions),
		Aliases:            marshalIDs(compiled.aliases),
		TriggerQuestions:   marshalIDs(compiled.triggerQuestions),
		UncoveredPoints:    marshalUncoveredPoints(compiled.uncoveredPoints),
		CompiledFrom:       marshalIDs(nonEmpty(req.ResultID)),
		PromptVersion:      "v1",
		ModelName:          "reasoning",
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
	if page.PageType == PageTypeTopic {
		return s.RecompileTopic(ctx, page, reason, compiledFrom)
	}

	conceptID := ""
	if page.ConceptID.Valid {
		conceptID = page.ConceptID.String
	}

	// Recompile has no exposed analyze-preview step (docs/impl/v1/wiki.md
	// 步骤 5): the human confirming "recompile" on the Page is itself the
	// confirmation of this new analysis round.
	claims, tensions, err := s.analyzeClaims(ctx, conceptID)
	if err != nil {
		return nil, err
	}
	compiled, err := s.compileContent(ctx, conceptID, page.PageType, claims, tensions)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(pageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalConditions(compiled.observedConditions),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalUncoveredPoints(compiled.uncoveredPoints),
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
	title              string
	content            string
	sourcePointIDs     []string
	sourceUnitIDs      []string
	sourceLinkIDs      []string
	observedConditions []activation.ObservedCondition
	aliases            []string
	triggerQuestions   []string
	uncoveredPoints    []UncoveredPoint
}

// marshalConditions JSON-encodes an observed_conditions union for storage,
// defaulting to "[]" like marshalIDs does for the other JSON-array fields.
// marshalUncoveredPoints JSON-encodes an uncovered_points list for storage,
// defaulting to "[]".
func marshalUncoveredPoints(points []UncoveredPoint) string {
	if len(points) == 0 {
		return "[]"
	}
	b, err := json.Marshal(points)
	if err != nil {
		slog.Warn("wiki: marshal uncovered points failed, storing empty", "error", err)
		return "[]"
	}
	return string(b)
}

func marshalConditions(conds []activation.ObservedCondition) string {
	if len(conds) == 0 {
		return "[]"
	}
	b, err := json.Marshal(conds)
	if err != nil {
		slog.Warn("wiki: marshal observed conditions failed, storing empty", "error", err)
		return "[]"
	}
	return string(b)
}

// compileInputs bundles the material-gathering step shared by analysis and
// generation (docs/impl/v1/wiki.md 步骤 3 "输入收集").
type compileInputs struct {
	conceptName  string
	conceptDesc  string
	qualifying   []QualifyingPoint
	materials    string
	usedPointIDs []string
	gapsText     string
}

func (s *Service) gatherInputs(conceptID string) (*compileInputs, error) {
	conceptName, conceptDesc, _, err := s.store.GetConceptInfo(conceptID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get concept info: %w", err)
	}

	qualifying, err := s.store.ListQualifyingPoints(conceptID)
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

	return &compileInputs{
		conceptName:  conceptName,
		conceptDesc:  conceptDesc,
		qualifying:   qualifying,
		materials:    materials,
		usedPointIDs: usedPointIDs,
		gapsText:     gapsText,
	}, nil
}

// analyzeClaims implements docs/impl/v1/wiki.md 步骤 3「分析产物」: call the
// analysis Prompt once (retrying once on validation failure) to get the
// proposed claim structure, validated against the full qualifying-KP
// whitelist. Shared by Analyze, Compile (debug/no-analysis path) and
// Recompile.
func (s *Service) analyzeClaims(ctx context.Context, conceptID string) ([]Claim, []Tension, error) {
	in, err := s.gatherInputs(conceptID)
	if err != nil {
		return nil, nil, err
	}

	whitelist := make(map[string]bool, len(in.usedPointIDs))
	for _, id := range in.usedPointIDs {
		whitelist[id] = true
	}

	vars := map[string]string{
		"concept_name":        in.conceptName,
		"concept_description": in.conceptDesc,
		"materials":           in.materials,
		"gaps":                in.gapsText,
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_analyze.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: analyze llm call failed", "attempt", attempt, "concept_id", conceptID, "error", err)
			continue
		}

		var output struct {
			Claims   []Claim   `json:"claims"`
			Tensions []Tension `json:"tensions"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: analyze parse failed", "attempt", attempt, "concept_id", conceptID, "error", err)
			continue
		}

		claims := filterClaims(output.Claims, whitelist, conceptID)
		tensions := filterTensions(output.Tensions, whitelist, conceptID)
		if len(claims) == 0 {
			lastErr = fmt.Errorf("wiki: analysis produced no usable claims")
			slog.Warn("wiki: analyze produced no usable claims", "attempt", attempt, "concept_id", conceptID)
			continue
		}
		return claims, tensions, nil
	}

	return nil, nil, fmt.Errorf("wiki: analyze failed after retry: %w", lastErr)
}

// filterClaims drops any cited_point_id outside whitelist (warn), then drops
// whole claims left with zero citations — an uncited claim can't be enforced
// by the generation whitelist downstream.
func filterClaims(claims []Claim, whitelist map[string]bool, conceptID string) []Claim {
	var out []Claim
	var droppedIDs []string
	droppedClaims := 0
	for _, c := range claims {
		var kept []string
		for _, id := range c.CitedPointIDs {
			if whitelist[id] {
				kept = append(kept, id)
			} else {
				droppedIDs = append(droppedIDs, id)
			}
		}
		if len(kept) == 0 {
			droppedClaims++
			continue
		}
		out = append(out, Claim{Summary: c.Summary, CitedPointIDs: kept})
	}
	if len(droppedIDs) > 0 {
		slog.Warn("wiki: dropped out-of-whitelist cited_point_ids in analysis", "concept_id", conceptID, "ids", droppedIDs)
	}
	if droppedClaims > 0 {
		slog.Warn("wiki: dropped claims with no whitelisted citations after filtering", "concept_id", conceptID, "count", droppedClaims)
	}
	return out
}

// filterTensions drops any related_point_id outside whitelist (warn); a
// tension with zero related points is still kept — it can describe a gap
// with no existing KP to point at.
func filterTensions(tensions []Tension, whitelist map[string]bool, conceptID string) []Tension {
	var out []Tension
	var dropped []string
	for _, t := range tensions {
		var kept []string
		for _, id := range t.RelatedPointIDs {
			if whitelist[id] {
				kept = append(kept, id)
			} else {
				dropped = append(dropped, id)
			}
		}
		out = append(out, Tension{Description: t.Description, RelatedPointIDs: kept})
	}
	if len(dropped) > 0 {
		slog.Warn("wiki: dropped out-of-whitelist related_point_ids in analysis", "concept_id", conceptID, "ids", dropped)
	}
	return out
}

// claimsWhitelist unions every confirmed claim's cited_point_ids — the
// generation-stage citation whitelist, narrower than the full qualifying-KP
// set used at analysis time (docs/design/wiki-compilation.md "编译内部分
// 两步").
func claimsWhitelist(claims []Claim) map[string]bool {
	w := make(map[string]bool)
	for _, c := range claims {
		for _, id := range c.CitedPointIDs {
			w[id] = true
		}
	}
	return w
}

// compileContent implements docs/impl/v1/wiki.md 步骤 3「生成产物」: given an
// already-confirmed claim structure, call the generation Prompt once
// (retrying once on validation failure) and validate the result. Shared by
// Compile and Recompile.
func (s *Service) compileContent(ctx context.Context, conceptID, pageType string, claims []Claim, tensions []Tension) (*compiledContent, error) {
	if len(claims) == 0 {
		return nil, fmt.Errorf("wiki: no confirmed claims for concept %s", conceptID)
	}

	in, err := s.gatherInputs(conceptID)
	if err != nil {
		return nil, err
	}

	pageTypeHint := "本页面类型为 concept（概念页）：标题使用概念名称本身。"
	if pageType == PageTypeTopic {
		pageTypeHint = "本页面类型为 topic（主题页）：标题请根据材料内容概括一个合适的主题名称，不必是概念名称本身。"
	}

	// Generation's citation whitelist is the claims' cited_point_ids union —
	// narrower than the full qualifying-KP set analysis was validated
	// against, by design.
	whitelist := claimsWhitelist(claims)

	claimsJSON, _ := json.Marshal(claims)
	tensionsJSON := "[]"
	if len(tensions) > 0 {
		if b, err := json.Marshal(tensions); err == nil {
			tensionsJSON = string(b)
		}
	}

	triggerMax := s.cfg.TriggerQuestionsMax
	if triggerMax <= 0 {
		triggerMax = 10
	}

	// Real observed questions ground trigger_questions in confirmed usage
	// instead of LLM invention (docs/design/wiki-compilation.md "触发问法取材
	// 真实观测，检索匹配复用四元组"). Best-effort: failure just means the LLM
	// falls back to inventing from materials, doesn't fail the compile.
	observedQuestions, err := s.store.ConfidentQuestionsForPoints(in.usedPointIDs, triggerMax)
	if err != nil {
		slog.Warn("wiki: fetch confident questions failed, continuing without them", "concept_id", conceptID, "error", err)
	}
	observedQuestionsText := "（无）"
	if len(observedQuestions) > 0 {
		observedQuestionsText = "- " + strings.Join(observedQuestions, "\n- ")
	}

	vars := map[string]string{
		"concept_name":        in.conceptName,
		"concept_description": in.conceptDesc,
		"materials":           in.materials,
		"claims":              string(claimsJSON),
		"tensions":            tensionsJSON,
		"observed_questions":  observedQuestionsText,
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

		sourceUnitIDs := sourceUnitsForPoints(citedInContent, in.qualifying)
		sourceLinkIDs, err := s.store.VerifiedLinkIDsForPoints(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup verified link ids for cited points failed", "concept_id", conceptID, "error", err)
		}
		observedConditions, err := s.store.VerifiedLinksObservedConditions(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup observed conditions for cited points failed", "concept_id", conceptID, "error", err)
		}

		if len(output.Aliases) == 0 || len(output.TriggerQuestions) == 0 {
			slog.Warn("wiki: compile output missing aliases/trigger_questions, storing empty",
				"concept_id", conceptID, "aliases", len(output.Aliases), "trigger_questions", len(output.TriggerQuestions))
		}

		uncoveredPoints, err := s.store.ListUncoveredPoints(conceptID)
		if err != nil {
			slog.Warn("wiki: list uncovered points failed, storing empty", "concept_id", conceptID, "error", err)
		}

		return &compiledContent{
			title:              output.Title,
			content:            filteredContent,
			sourcePointIDs:     citedInContent,
			sourceUnitIDs:      sourceUnitIDs,
			sourceLinkIDs:      sourceLinkIDs,
			observedConditions: observedConditions,
			aliases:            truncateStrings(output.Aliases, triggerMax),
			triggerQuestions:   truncateStrings(output.TriggerQuestions, triggerMax),
			uncoveredPoints:    uncoveredPoints,
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

	if page.PageType == PageTypeConcept {
		if err := s.RecomputeRelationsForPage(pageID); err != nil {
			slog.Error("wiki: recompute relations after publish failed", "page_id", pageID, "error", err)
		}
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
	s.clearRelationsForPage(pageID)
	s.cascadeToParentTopics(pageID, "archived")

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
	s.cascadeToParentTopics(pageID, reason)
	return nil
}

// cascadeToParentTopics implements docs/impl/v1/wiki.md 步骤 9: a concept
// page entering needs_recompile or archived propagates to its containing
// topic page(s) (needs_recompile only — topic pages don't propagate further,
// "只有两层"). No-op for topic pages themselves (contains is only ever
// concept -> topic in the ContainingTopics lookup direction, so this is
// naturally a no-op when called with a topic page id, but the page_type
// check makes the intent explicit and avoids an extra store round-trip).
func (s *Service) cascadeToParentTopics(memberPageID, memberReason string) {
	page, err := s.store.GetPage(memberPageID)
	if err != nil || page == nil || page.PageType != PageTypeConcept {
		return
	}
	topics, err := s.store.ContainingTopics(memberPageID)
	if err != nil {
		slog.Warn("wiki: cascade to parent topics: list containing topics failed", "page_id", memberPageID, "error", err)
		return
	}
	for _, topicID := range topics {
		if err := s.MarkNeedsRecompile(topicID, "member_page_changed:"+memberPageID); err != nil {
			slog.Error("wiki: cascade needs_recompile to parent topic failed", "topic_page_id", topicID, "member_page_id", memberPageID, "error", err)
		}
	}
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
// fresh count of qualifying KPs for that concept, same query semantics as
// this package's own qualifying-KP query) against the
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
// SkeletonInfo carries the topic-page recall skeleton
// (docs/impl/v1/wiki.md 步骤 8「检索接入」): the point_ids of every member
// concept page a topic-page hit expanded into (including members truncated
// out of the direct-answer candidate list), plus the topic page id that
// provided it. Set whenever a topic page was hit during candidate gathering,
// regardless of whether direct answer ultimately succeeded — traces.
// skeleton_page_id records it either way (docs/impl/v1/wiki.md 步骤 8:
// "无论直答是否成功都记录").
type SkeletonInfo struct {
	PageID  string
	Members []SkeletonMember
}

// SkeletonMember is one expanded topic-page member and its own
// source_point_ids (not the flat union) — callers that need per-member
// attribution (docs/impl/v1/trace.md topic_decompose_signal's
// resolved_member_page_ids) can use this without a second lookup.
type SkeletonMember struct {
	PageID   string
	PointIDs []string
}

func (s *Service) TryDirectAnswer(ctx context.Context, question, subject, intent, audience, constraint string, minScore float64, maxCandidates int) (*DirectAnswerResult, bool, *SkeletonInfo, error) {
	if maxCandidates <= 0 {
		maxCandidates = 3
	}

	candidates, skeleton, err := s.gatherDirectAnswerCandidates(question, subject, intent, audience, constraint, minScore, maxCandidates)
	if err != nil {
		return nil, false, nil, err
	}

	for _, pageID := range candidates {
		page, err := s.store.GetPage(pageID)
		if err != nil {
			return nil, false, skeleton, fmt.Errorf("wiki: get page: %w", err)
		}
		if page == nil || page.Status != StatusPublished {
			// Index/DB momentarily out of sync (e.g. mid recompile) — skip.
			continue
		}

		result, ok, err := s.answerFromPage(ctx, question, page)
		if err != nil {
			return nil, false, skeleton, err
		}
		if ok {
			return result, true, skeleton, nil
		}
	}
	return nil, false, skeleton, nil
}

// gatherDirectAnswerCandidates implements docs/impl/v1/wiki.md 步骤 4's three
// direct-answer entries, merged and deduped, in priority order: four-tuple
// hits (most-verified-specific — precise match against previously confirmed
// usage) first, then lexical hits (wiki index, including aliases/
// trigger_questions fields, score >= minScore) ordered by score, then
// concept-name hits not already present, truncated to maxCandidates. None of
// the three entries calls the LLM.
func (s *Service) gatherDirectAnswerCandidates(question, subject, intent, audience, constraint string, minScore float64, maxCandidates int) ([]string, *SkeletonInfo, error) {
	seen := make(map[string]bool)
	var rawHits []string // may include topic pages, pre-expansion

	fourTupleHits, err := s.matchFourTupleEntry(subject, intent, audience, constraint)
	if err != nil {
		slog.Warn("wiki: four-tuple entry lookup failed, continuing without it", "error", err)
	}
	for _, pageID := range fourTupleHits {
		if seen[pageID] {
			continue
		}
		seen[pageID] = true
		rawHits = append(rawHits, pageID)
	}

	q := bleve.NewMatchQuery(question)
	req := bleve.NewSearchRequest(q)
	req.Size = maxCandidates

	results, err := s.wikiIndex.Search(req)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: search: %w", err)
	}

	for _, hit := range results.Hits {
		if hit.Score < minScore || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		rawHits = append(rawHits, hit.ID)
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
		rawHits = append(rawHits, pageID)
	}

	// Topic-page expansion (docs/impl/v1/wiki.md 步骤 8「检索接入」): a topic
	// page never enters the direct-answer sequence itself — it's a recall
	// skeleton, not an answer unit. Expand it into its published contains
	// members, inserted at the slot the topic page held; a member already
	// present from another entry keeps its existing (higher-priority) slot.
	var candidates []string
	seenConcept := make(map[string]bool)
	var skeleton *SkeletonInfo
	for _, pageID := range rawHits {
		page, err := s.store.GetPage(pageID)
		if err != nil || page == nil {
			continue
		}
		if page.PageType != PageTypeTopic {
			if !seenConcept[pageID] {
				seenConcept[pageID] = true
				candidates = append(candidates, pageID)
			}
			continue
		}

		orderedMemberIDs, members, err := s.expandTopicMembers(pageID, question)
		if err != nil {
			slog.Warn("wiki: expand topic members failed", "page_id", pageID, "error", err)
			continue
		}
		if skeleton == nil {
			skeleton = &SkeletonInfo{PageID: pageID}
		}
		skeleton.Members = append(skeleton.Members, members...)
		for _, m := range orderedMemberIDs {
			if !seenConcept[m] {
				seenConcept[m] = true
				candidates = append(candidates, m)
			}
		}
	}

	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	return candidates, skeleton, nil
}

// expandTopicMembers implements docs/impl/v1/wiki.md 步骤 8's member
// ordering: published contains members ranked by question-term overlap with
// member_roles.question_types (falling back to source_point_ids count
// descending when member_roles is empty). Returns the ordered member page
// ids plus the union of ALL members' source_point_ids — including ones that
// get truncated out of the direct-answer candidate list — for
// skeleton_point_ids (docs/impl/v1/wiki.md 步骤 8: "含被截断掉的").
func (s *Service) expandTopicMembers(topicPageID, question string) (memberIDs []string, members []SkeletonMember, err error) {
	page, err := s.store.GetPage(topicPageID)
	if err != nil || page == nil {
		return nil, nil, err
	}
	var roles []MemberRole
	json.Unmarshal([]byte(page.MemberRoles), &roles)
	roleByMember := make(map[string]MemberRole, len(roles))
	for _, r := range roles {
		roleByMember[r.MemberPageID] = r
	}

	memberPageIDs, err := s.store.ContainsMembers(topicPageID)
	if err != nil {
		return nil, nil, err
	}

	type scored struct {
		pageID   string
		score    int
		kpN      int
		pointIDs []string
	}
	questionTerms := text.TermSet(question)
	var published []scored
	for _, m := range memberPageIDs {
		mp, err := s.store.GetPage(m)
		if err != nil || mp == nil || mp.Status != StatusPublished {
			continue
		}
		var mPointIDs []string
		json.Unmarshal([]byte(mp.SourcePointIDs), &mPointIDs)

		sc := scored{pageID: m, kpN: len(mPointIDs), pointIDs: mPointIDs}
		if role, ok := roleByMember[m]; ok {
			overlap := 0
			for _, qt := range role.QuestionTypes {
				qtTerms := text.TermSet(qt)
				for t := range qtTerms {
					if _, ok := questionTerms[t]; ok {
						overlap++
					}
				}
			}
			sc.score = overlap
		}
		published = append(published, sc)
	}

	sort.SliceStable(published, func(i, j int) bool {
		if published[i].score != published[j].score {
			return published[i].score > published[j].score
		}
		return published[i].kpN > published[j].kpN
	})

	for _, sc := range published {
		memberIDs = append(memberIDs, sc.pageID)
		members = append(members, SkeletonMember{PageID: sc.pageID, PointIDs: sc.pointIDs})
	}
	return memberIDs, members, nil
}

// matchFourTupleEntry implements docs/impl/v1/wiki.md 步骤 4c: match the
// already-Session-parsed subject/intent/audience/constraint against every
// published page's aggregated observed_conditions, reusing
// activation.MatchConditionGroups instead of a second matching
// implementation (docs/design/wiki-compilation.md "触发问法取材真实观测，
// 检索匹配复用四元组"). Naturally no-ops when all four fields are empty (the
// plain POST /answer path that skips Session parsing) — MatchConditionGroups
// itself guards against an all-empty query. Ties among multiple matching
// pages break by the matching group's LastSeenAt descending, mirroring
// Matcher.Match's LastUsedAt-descending sort.
func (s *Service) matchFourTupleEntry(subject, intent, audience, constraint string) ([]string, error) {
	if s.activationSvc == nil {
		return nil, nil
	}
	if subject == "" && intent == "" && audience == "" && constraint == "" {
		return nil, nil
	}

	resolver, err := s.activationSvc.LoadSynonymResolver()
	if err != nil {
		return nil, fmt.Errorf("wiki: load synonym resolver: %w", err)
	}
	queryTopic, qi, qa, qc := activation.BuildQueryConditionTerms(subject, intent, audience, constraint, resolver)

	pages, err := s.store.ListPublishedPagesWithConditions()
	if err != nil {
		return nil, fmt.Errorf("wiki: list published pages with conditions: %w", err)
	}

	type match struct {
		pageID     string
		lastSeenAt time.Time
	}
	var matches []match
	for _, p := range pages {
		var latest time.Time
		hit := false
		for _, cond := range p.Conditions {
			if !activation.MatchConditionGroups([]activation.ObservedCondition{cond}, queryTopic, qi, qa, qc, resolver) {
				continue
			}
			hit = true
			if cond.LastSeenAt.After(latest) {
				latest = cond.LastSeenAt
			}
		}
		if hit {
			matches = append(matches, match{pageID: p.PageID, lastSeenAt: latest})
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].lastSeenAt.After(matches[j].lastSeenAt)
	})

	pageIDs := make([]string, len(matches))
	for i, m := range matches {
		pageIDs[i] = m.pageID
	}
	return pageIDs, nil
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
