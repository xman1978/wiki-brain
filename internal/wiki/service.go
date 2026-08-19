package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// ManualTriggerSentinel marks Page.CompiledFrom when a concept-page compile
// was triggered by a human picking a concept directly (docs/impl/v1/wiki.md
// 步骤 2 "人工指定主题手动编译"), not by a Study wiki_candidate result_id —
// distinguishes the two origins in the same field, without a new column
// (mirrors the existing precedent of compiled_from already holding
// learning_result ids; this is just another kind of id-shaped provenance
// marker in the same slot).
const ManualTriggerSentinel = "manual_trigger"

var requiredSections = []string{"## 摘要", "## 稳定结论", "## 展开说明", "## 待验证点", "## 依赖来源"}

var pointIDTagRe = regexp.MustCompile(`\[([^\[\]\s]+)\]`)

type Service struct {
	store         *Store
	llmClient     llm.LLMClient
	wikiIndex     bleve.Index
	pointsIndex   bleve.Index
	outlinesIndex bleve.Index
	cfg           config.WikiConfig
	activationSvc *activation.Service
}

func NewService(store *Store, llmClient llm.LLMClient, wikiIndex, pointsIndex, outlinesIndex bleve.Index, cfg config.WikiConfig) *Service {
	return &Service{store: store, llmClient: llmClient, wikiIndex: wikiIndex, pointsIndex: pointsIndex, outlinesIndex: outlinesIndex, cfg: cfg}
}

// SetActivationSvc wires the (optional) dependency Compile needs to resolve
// the pending_confirm wiki_candidate learning_result (docs/impl/v1/wiki.md
// 步骤 2). Compile still works without it (result_id resolution just no-ops).
func (s *Service) SetActivationSvc(a *activation.Service) {
	s.activationSvc = a
}

// Analyze implements docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3:
// POST /wiki/compile/analyze. Read-only — it changes no state and its result
// is never persisted. The caller is expected to show it to a human and, on
// confirmation, send it back as CompileRequest.Claims/Tensions
// (docs/design/wiki-compilation.md "编译内部分两步").
func (s *Service) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResult, error) {
	if len(req.EntryIDs) == 0 {
		return nil, fmt.Errorf("wiki: entry_ids is required")
	}

	in, err := s.gatherSubgraphInputs(req.EntryIDs, pointIDSet(req.PointIDs))
	if err != nil {
		return nil, err
	}

	claims, tensions, err := s.analyzeClaimsWithInputs(ctx, req.EntryIDs, in)
	if err != nil {
		return nil, err
	}
	return &AnalyzeResult{
		EntryIDs: req.EntryIDs,
		ResultID: req.ResultID,
		Claims:   claims,
		Tensions: tensions,
	}, nil
}

// Compile implements docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3:
// POST /wiki/compile. Single-tier: one call over a human-picked entry_id set
// produces one finished page directly (no concept-page/topic-page tiering,
// no second compile step).
func (s *Service) Compile(ctx context.Context, req CompileRequest) (*Page, error) {
	if len(req.EntryIDs) == 0 {
		return nil, fmt.Errorf("wiki: entry_ids is required")
	}

	if req.ResultID != "" {
		if s.activationSvc != nil {
			if err := s.activationSvc.Store().ResolvePending(req.ResultID, activation.ResultApplied, "manual"); err != nil {
				slog.Warn("wiki: resolve pending wiki_candidate result failed", "result_id", req.ResultID, "error", err)
			}
		}
	} else {
		slog.Info("wiki: compile triggered without result_id (manual trigger)", "entry_ids", req.EntryIDs)
	}

	for _, entryID := range req.EntryIDs {
		existing, err := s.store.GetActivePageByEntryID(entryID)
		if err != nil {
			return nil, fmt.Errorf("wiki: check existing page: %w", err)
		}
		if existing != nil {
			return nil, ErrPageAlreadyExists
		}
	}

	pointFilter := pointIDSet(req.PointIDs)
	claims, tensions := req.Claims, req.Tensions
	if len(claims) == 0 {
		// No analysis round-tripped back (caller skipped
		// /wiki/compile/analyze entirely) — run it internally so generation
		// is still constrained to an analysis result, not raw material
		// access.
		var err error
		claims, tensions, err = s.analyzeClaims(ctx, req.EntryIDs, pointFilter)
		if err != nil {
			return nil, err
		}
	}

	compiled, err := s.compileSubgraphContent(ctx, req.EntryIDs, claims, tensions, pointFilter)
	if err != nil {
		return nil, err
	}

	compiledFromIDs := nonEmpty(req.ResultID)
	if len(compiledFromIDs) == 0 {
		// No Study wiki_candidate result_id round-tripped back — a human
		// picked these entries directly. Recorded so pages can later be told
		// apart by origin without a new column (see ManualTriggerSentinel).
		compiledFromIDs = []string{ManualTriggerSentinel}
	}

	page := &Page{
		// Single-tier Wiki has only one page_type — every compiled page
		// shares the existing "topic" name/const (docs/impl/v1/
		// wiki-single-tier-task-brief.md 步骤 3: "所有新编译页面 page_type
		// 统一写 PageTypeTopic").
		PageType:           PageTypeTopic,
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
		CompiledFrom:       marshalIDs(compiledFromIDs),
		Summary:            compiled.summary,
		Aspects:            "[]",
		PromptVersion:      "v1",
		ModelName:          "reasoning",
	}
	// Page.entry_id remains a single nullable column, storing the first
	// entry_id as the page's primary identity for catalog's existing
	// domain-grouped JOIN. The FULL entry_id set is written to
	// wiki_page_entries in the same transaction (migration 057,
	// docs/impl/v1/wiki-single-tier-open-questions.md「已拍板（2026-08-18）」
	// 第 1 条) so Recompile can rebuild the Core/Context/Conflict subgraph
	// from every entry_id this page was compiled from, not just the primary
	// one.
	page.EntryID = nullableString(req.EntryIDs[0])

	if err := s.store.InsertPageWithEntries(page, req.EntryIDs); err != nil {
		return nil, err
	}
	rev := &Revision{PageID: page.PageID, Content: page.Content, Title: page.Title, Reason: "compile"}
	if err := s.store.InsertRevision(rev); err != nil {
		slog.Error("wiki: insert initial revision failed", "page_id", page.PageID, "error", err)
	} else {
		s.verifyClaims(ctx, page.PageID, rev.RevisionID, req.EntryIDs, claims, pointFilter)
	}

	slog.Info("wiki: compiled draft page", "page_id", page.PageID, "entry_ids", req.EntryIDs,
		"source_point_ids", len(compiled.sourcePointIDs))
	return s.store.GetPage(page.PageID)
}

// Recompile implements docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3:
// re-run compilation for an existing (non-archived) page, writing a new
// revision and resetting it to draft — the caller must publish again.
//
// entryIDs to rebuild the subgraph from are read from wiki_page_entries
// (migration 057, docs/impl/v1/wiki-single-tier-open-questions.md「已拍板
// （2026-08-18）」第 1 条) — the full set the page was originally compiled
// from, not just page.EntryID's single primary entry. Falls back to
// page.EntryID for pages inserted before migration 057 (no
// wiki_page_entries rows yet).
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

	entryIDs, err := s.store.EntryIDsByPageID(pageID)
	if err != nil {
		return nil, err
	}
	if len(entryIDs) == 0 && page.EntryID.Valid && page.EntryID.String != "" {
		entryIDs = []string{page.EntryID.String}
	}
	if len(entryIDs) == 0 {
		return nil, fmt.Errorf("wiki: page %s has no entry_id to recompile from", pageID)
	}

	// Recompile has no exposed analyze-preview step: the human confirming
	// "recompile" on the Page is itself the confirmation of this new
	// analysis round. Always a full page regeneration — no per-section
	// incremental diffing (docs/impl/v1/wiki-generation.md 第 8 节, 明确不做).
	claims, tensions, err := s.analyzeClaims(ctx, entryIDs, nil)
	if err != nil {
		return nil, err
	}
	compiled, err := s.compileSubgraphContent(ctx, entryIDs, claims, tensions, nil)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(pageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalConditions(compiled.observedConditions),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalUncoveredPoints(compiled.uncoveredPoints),
		marshalIDs(compiledFrom), compiled.summary, "[]", "v1", "reasoning"); err != nil {
		return nil, err
	}
	rev := &Revision{PageID: pageID, Content: compiled.content, Title: compiled.title, Reason: reason}
	if err := s.store.InsertRevision(rev); err != nil {
		slog.Error("wiki: insert recompile revision failed", "page_id", pageID, "error", err)
	} else {
		s.verifyClaims(ctx, pageID, rev.RevisionID, entryIDs, claims, nil)
	}

	// A page must not answer directly while it's being recompiled/awaiting
	// re-publish under a stale index entry.
	if err := s.wikiIndex.Delete(pageID); err != nil {
		slog.Warn("wiki: remove page from index after recompile failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: recompiled page", "page_id", pageID, "reason", reason)
	return s.store.GetPage(pageID)
}

// compiledContent is compileSubgraphContent's output — one fully-assembled
// page generated in a single LLM call (docs/impl/v1/wiki-generation.md 阶段
// D/F, materials restructured per docs/impl/v1/wiki-single-tier-task-brief.md
// 步骤 3 to Core/Context/Conflict).
type compiledContent struct {
	title              string
	content            string
	summary            string // extracted "## 摘要" section text, mirrors Page.Summary
	sourcePointIDs     []string
	sourceUnitIDs      []string
	sourceLinkIDs      []string
	observedConditions []activation.ObservedCondition
	aliases            []string
	triggerQuestions   []string
	uncoveredPoints    []UncoveredPoint
}

// entryKindLabel returns the Chinese page-kind label injected as
// {{entry_kind_label}}: 概念 or 事实. Unknown/empty kind defaults to 概念,
// matching ValidateEntryKind / pageTypeForKind. For a multi-entry compile
// this reflects only the first entry_id's kind — a wording nudge only, it
// does not change page_type (always PageTypeTopic now) or any gate.
func entryKindLabel(kind string) string {
	if kind == "fact" {
		return "事实"
	}
	return "概念"
}

// conceptKindHint renders the analyze/compile prompts' {{entry_kind_hint}}
// var from the (first) entry's kind — a wording nudge only.
func conceptKindHint(kind string) string {
	switch kind {
	case "fact":
		return "这是一个事实页，围绕这个具体、可唯一指认的对象组织论断，claim 应回答'这是什么、当前状态如何、和哪些概念或其他事实存在关联'，不要泛化成脱离这个具体对象的通用规律。"
	default:
		return "这是一个概念页，围绕通用规律/原理/规则组织论断，claim 应回答'这个概念的定义、边界、内部逻辑是什么'，不要陷入某一个具体实现的细节。"
	}
}

// analyzeInputs bundles the analysis stage's material-gathering output
// (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3): Core/Context/Conflict
// KP groups instead of aspect-clustering groups.
type analyzeInputs struct {
	entryIDs     []string
	entryName    string // joined display names of every compiled entry_id
	entryDesc    string // first entry_id's description
	entryKind    string // first entry_id's kind, for entry_kind_label/hint
	domainName   string
	core         []QualifyingPoint
	context      []QualifyingPoint
	conflict     []QualifyingPoint
	coreText     string
	contextText  string
	conflictText string
	whitelist    map[string]bool // union of core/context/conflict point_ids
	byID         map[string]QualifyingPoint
	gapsText     string
}

// gatherSubgraphInputs implements docs/impl/v1/wiki-single-tier-task-brief.md
// 步骤 3: build the Core/Context/Conflict subgraph for entryIDs and render it
// into the analyze/compile prompts' material text. Citation whitelist =
// every point_id the subgraph covers (Core ∪ Context ∪ Conflict) — no
// budget truncation (design doc "已拍板" 第 3 条: Core is unbounded, and the
// one-hop expansion is itself the size control).
// pointIDSet converts the optional human-picked KP whitelist (AnalyzeRequest/
// CompileRequest.PointIDs) into the map buildKnowledgeSubgraph filters
// Core against; an empty slice returns nil so "no restriction" stays the
// literal zero value throughout the call chain instead of an empty-but-non-nil
// map (both behave the same via len() checks, nil is just the more honest
// signal for "wasn't provided").
func pointIDSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func (s *Service) gatherSubgraphInputs(entryIDs []string, pointFilter map[string]bool) (*analyzeInputs, error) {
	core, context, conflict, err := s.buildKnowledgeSubgraph(entryIDs, pointFilter)
	if err != nil {
		return nil, err
	}

	var names []string
	var firstDesc, firstKind, domainID string
	for i, id := range entryIDs {
		name, desc, dom, kind, _, err := s.store.GetEntryInfo(id)
		if err != nil {
			return nil, fmt.Errorf("wiki: get entry info: %w", err)
		}
		names = append(names, name)
		if i == 0 {
			firstDesc, firstKind, domainID = desc, kind, dom
		}
	}
	domainName, err := s.store.GetDomainName(domainID)
	if err != nil {
		slog.Warn("wiki: get domain name failed, continuing without it", "entry_ids", entryIDs, "error", err)
	}

	whitelist := make(map[string]bool)
	byID := make(map[string]QualifyingPoint)
	for _, group := range [][]QualifyingPoint{core, context, conflict} {
		for _, p := range group {
			whitelist[p.PointID] = true
			byID[p.PointID] = p
		}
	}

	allPoints := make([]QualifyingPoint, 0, len(core)+len(context)+len(conflict))
	allPoints = append(allPoints, core...)
	allPoints = append(allPoints, context...)
	allPoints = append(allPoints, conflict...)
	gapsText, err := s.matchingGaps(strings.Join(names, "、"), allPoints)
	if err != nil {
		slog.Warn("wiki: gather gaps failed, continuing without them", "entry_ids", entryIDs, "error", err)
	}

	return &analyzeInputs{
		entryIDs:     entryIDs,
		entryName:    strings.Join(names, "、"),
		entryDesc:    firstDesc,
		entryKind:    firstKind,
		domainName:   domainName,
		core:         core,
		context:      context,
		conflict:     conflict,
		coreText:     renderSubgraphGroup(core),
		contextText:  renderSubgraphGroup(context),
		conflictText: renderSubgraphGroup(conflict),
		whitelist:    whitelist,
		byID:         byID,
		gapsText:     gapsText,
	}, nil
}

// renderSubgraphGroup formats one Core/Context/Conflict group's material for
// the analyze/compile prompts — Core points borrowed from a fact entry's
// parent Concept are tagged so the model can tell "本页核心" apart from
// "父概念背景" (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 3 第 2 条).
func renderSubgraphGroup(points []QualifyingPoint) string {
	var sb strings.Builder
	for _, p := range points {
		tag := ""
		if p.SubgraphRole == SubgraphRoleCoreParentBackground {
			tag = "（父概念背景）"
		}
		fmt.Fprintf(&sb, "  [%s]%s %s\n", p.PointID, tag, p.Content)
	}
	return sb.String()
}

// analyzeClaims implements docs/impl/v1/wiki-generation.md 阶段 C: call the
// analysis Prompt once (retrying once on validation failure) to get the
// proposed claim structure, validated against the subgraph citation
// whitelist. Shared by Analyze, Compile (no-analysis path) and Recompile.
func (s *Service) analyzeClaims(ctx context.Context, entryIDs []string, pointFilter map[string]bool) ([]Claim, []Tension, error) {
	in, err := s.gatherSubgraphInputs(entryIDs, pointFilter)
	if err != nil {
		return nil, nil, err
	}
	return s.analyzeClaimsWithInputs(ctx, entryIDs, in)
}

// analyzeClaimsWithInputs is analyzeClaims split so Analyze() can reuse the
// same *analyzeInputs it already built.
func (s *Service) analyzeClaimsWithInputs(ctx context.Context, entryIDs []string, in *analyzeInputs) ([]Claim, []Tension, error) {
	vars := map[string]string{
		"entry_name":        in.entryName,
		"entry_description": in.entryDesc,
		"domain_name":       in.domainName,
		"core_material":     in.coreText,
		"context_material":  in.contextText,
		"conflict_material": in.conflictText,
		"gaps":              in.gapsText,
		"entry_kind_label":  entryKindLabel(in.entryKind),
		"entry_kind_hint":   conceptKindHint(in.entryKind),
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_analyze.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: analyze llm call failed", "attempt", attempt, "entry_ids", entryIDs, "error", err)
			continue
		}

		var output struct {
			Claims   []Claim   `json:"claims"`
			Tensions []Tension `json:"tensions"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: analyze parse failed", "attempt", attempt, "entry_ids", entryIDs, "error", err)
			continue
		}

		claims := filterClaims(output.Claims, in.whitelist, entryIDs)
		tensions := filterTensions(output.Tensions, in.whitelist, entryIDs)
		if len(claims) == 0 {
			lastErr = fmt.Errorf("wiki: analysis produced no usable claims")
			slog.Warn("wiki: analyze produced no usable claims", "attempt", attempt, "entry_ids", entryIDs)
			continue
		}

		return claims, tensions, nil
	}

	return nil, nil, fmt.Errorf("wiki: analyze failed after retry: %w", lastErr)
}

// extractSection pulls the text between a "## heading" line and the next
// "## " heading (or end of content), trimmed — used to lift Page.Summary out
// of the compiled Markdown's "## 摘要" section (docs/impl/v1/
// wiki-generation.md 6.2: the summary column is a string-extraction of the
// generated body, not a second LLM output).
func extractSection(content, heading string) string {
	start := strings.Index(content, heading)
	if start < 0 {
		return ""
	}
	rest := content[start+len(heading):]
	end := strings.Index(rest, "\n## ")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// compileSubgraphContent implements docs/impl/v1/wiki-single-tier-task-brief.md
// 步骤 3: given already-confirmed claims/tensions, call the generation
// Prompt once (retrying once on validation failure) over the Core/Context/
// Conflict subgraph material, and validate the result. Shared by Compile and
// Recompile.
func (s *Service) compileSubgraphContent(ctx context.Context, entryIDs []string, claims []Claim, tensions []Tension, pointFilter map[string]bool) (*compiledContent, error) {
	if len(claims) == 0 {
		return nil, fmt.Errorf("wiki: no confirmed claims for entries %v", entryIDs)
	}

	in, err := s.gatherSubgraphInputs(entryIDs, pointFilter)
	if err != nil {
		return nil, err
	}

	// Citation whitelist = the entire subgraph (Core ∪ Context ∪ Conflict),
	// not narrowed to only what confirmed claims already cite — the compile
	// prompt sees the full subgraph material and may legitimately elaborate
	// with additional supporting point_ids (docs/design/
	// wiki-single-tier-revision.md "citation 白名单 = Subgraph 覆盖的全部
	// point_id").
	whitelist := in.whitelist

	claimsJSON, _ := json.Marshal(claims)
	tensionsJSON := "[]"
	if len(tensions) > 0 {
		if b, err := json.Marshal(tensions); err == nil {
			tensionsJSON = string(b)
		}
	}

	vars := map[string]string{
		"entry_name":        in.entryName,
		"entry_description": in.entryDesc,
		"claims":            string(claimsJSON),
		"tensions":          tensionsJSON,
		"core_material":     in.coreText,
		"context_material":  in.contextText,
		"conflict_material": in.conflictText,
		"gaps":              in.gapsText,
		"entry_kind_label":  entryKindLabel(in.entryKind),
		"entry_kind_hint":   conceptKindHint(in.entryKind),
	}

	triggerMax := s.cfg.TriggerQuestionsMax
	if triggerMax <= 0 {
		triggerMax = 10
	}

	// aliases/trigger_questions are not LLM output (docs/design/
	// wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组" 生成侧
	// 修订): both are retrieval metadata backed by data the system already
	// has verified, not knowledge expression — best-effort, failure just
	// means the page stores an empty list for that field. Multi-entry
	// compile unions every entry_id's own aliases.
	var aliases []string
	seenAlias := make(map[string]bool)
	for _, name := range strings.Split(in.entryName, "、") {
		as, err := s.store.EntryAliases(name)
		if err != nil {
			slog.Warn("wiki: fetch concept aliases failed, continuing without them", "entry_name", name, "error", err)
			continue
		}
		for _, a := range as {
			if !seenAlias[a] {
				seenAlias[a] = true
				aliases = append(aliases, a)
			}
		}
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		content, err := s.llmClient.Complete(ctx, "wiki_compile.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: compile llm call failed", "attempt", attempt, "entry_ids", entryIDs, "error", err)
			continue
		}

		filteredContent, citedInContent, stripped := filterContentTags(content, whitelist)
		if len(stripped) > 0 {
			slog.Warn("wiki: stripped out-of-whitelist point_id tags from content", "entry_ids", entryIDs, "ids", stripped)
		}

		if !hasRequiredSections(filteredContent) {
			lastErr = fmt.Errorf("wiki: compiled content missing required sections")
			slog.Warn("wiki: compile missing required sections", "attempt", attempt, "entry_ids", entryIDs)
			continue
		}
		if len(citedInContent) == 0 {
			lastErr = fmt.Errorf("wiki: compiled content has no whitelisted citations")
			slog.Warn("wiki: compile produced no usable citations", "attempt", attempt, "entry_ids", entryIDs)
			continue
		}

		allPoints := make([]QualifyingPoint, 0, len(in.byID))
		for _, p := range in.byID {
			allPoints = append(allPoints, p)
		}
		sourceUnitIDs := sourceUnitsForPoints(citedInContent, allPoints)
		sourceLinkIDs, err := s.store.VerifiedLinkIDsForPoints(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup verified link ids for cited points failed", "entry_ids", entryIDs, "error", err)
		}
		observedConditions, err := s.store.VerifiedLinksObservedConditions(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup observed conditions for cited points failed", "entry_ids", entryIDs, "error", err)
		}
		triggerQuestions, err := s.store.ConfidentQuestionsForPoints(citedInContent, triggerMax)
		if err != nil {
			slog.Warn("wiki: fetch confident questions failed, storing empty", "entry_ids", entryIDs, "error", err)
		}

		var uncoveredPoints []UncoveredPoint
		seenUncovered := make(map[string]bool)
		for _, entryID := range entryIDs {
			ups, err := s.store.ListUncoveredPoints(entryID)
			if err != nil {
				slog.Warn("wiki: list uncovered points failed, continuing without them", "entry_id", entryID, "error", err)
				continue
			}
			for _, u := range ups {
				if !seenUncovered[u.PointID] {
					seenUncovered[u.PointID] = true
					uncoveredPoints = append(uncoveredPoints, u)
				}
			}
		}

		if len(aliases) == 0 || len(triggerQuestions) == 0 {
			slog.Info("wiki: no programmatic aliases/trigger_questions available, storing empty",
				"entry_ids", entryIDs, "aliases", len(aliases), "trigger_questions", len(triggerQuestions))
		}

		return &compiledContent{
			title:              in.entryName,
			content:            filteredContent,
			summary:            extractSection(filteredContent, "## 摘要"),
			sourcePointIDs:     citedInContent,
			sourceUnitIDs:      sourceUnitIDs,
			sourceLinkIDs:      sourceLinkIDs,
			observedConditions: observedConditions,
			aliases:            truncateStrings(aliases, triggerMax),
			triggerQuestions:   truncateStrings(triggerQuestions, triggerMax),
			uncoveredPoints:    uncoveredPoints,
		}, nil
	}

	return nil, fmt.Errorf("wiki: compile failed after retry: %w", lastErr)
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

// filterClaims drops any cited_point_id outside whitelist (warn), then drops
// whole claims left with zero citations — an uncited claim can't be enforced
// by the generation whitelist downstream.
func filterClaims(claims []Claim, whitelist map[string]bool, entryIDs []string) []Claim {
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
		slog.Warn("wiki: dropped out-of-whitelist cited_point_ids in analysis", "entry_ids", entryIDs, "ids", droppedIDs)
	}
	if droppedClaims > 0 {
		slog.Warn("wiki: dropped claims with no whitelisted citations after filtering", "entry_ids", entryIDs, "count", droppedClaims)
	}
	return out
}

// filterTensions drops any related_point_id outside whitelist (warn); a
// tension with zero related points is still kept — it can describe a gap
// with no existing KP to point at.
func filterTensions(tensions []Tension, whitelist map[string]bool, entryIDs []string) []Tension {
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
		slog.Warn("wiki: dropped out-of-whitelist related_point_ids in analysis", "entry_ids", entryIDs, "ids", dropped)
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

// verifyClaims implements docs/impl/v1/wiki-generation.md 阶段 E: an
// independent check of whether each confirmed claim's text is actually
// supported by the KP material it cites — orthogonal to (and run after) the
// existing citation whitelist check, which only verifies the cited
// point_ids are in-bounds, not that they support the claim's content
// (docs/design/wiki-compilation.md "编译产物的支持度核验"). Best-effort and
// non-blocking here: an LLM/parse failure logs a warning and produces no
// rows; results only gate Publish, via Selfcheck's
// UnsupportedClaimCount lookup, separately (so a verify failure never fails
// the compile itself). No-op when wiki.claim_verify_enabled is off.
func (s *Service) verifyClaims(ctx context.Context, pageID, revisionID string, entryIDs []string, claims []Claim, pointFilter map[string]bool) {
	if !s.cfg.ClaimVerifyEnabled || len(claims) == 0 {
		return
	}

	core, context, conflict, err := s.buildKnowledgeSubgraph(entryIDs, pointFilter)
	if err != nil {
		slog.Warn("wiki: claim verify build subgraph failed, skipping", "page_id", pageID, "error", err)
		return
	}
	pointContent := make(map[string]string)
	for _, group := range [][]QualifyingPoint{core, context, conflict} {
		for _, p := range group {
			pointContent[p.PointID] = p.Content
		}
	}

	type claimRef struct {
		id    string
		claim Claim
	}
	refs := make([]claimRef, len(claims))
	var sb strings.Builder
	for i, c := range claims {
		id := fmt.Sprintf("claim-%d", i+1)
		refs[i] = claimRef{id: id, claim: c}
		fmt.Fprintf(&sb, "【%s】结论：%s\n依据材料：\n", id, c.Summary)
		for _, pid := range c.CitedPointIDs {
			content := pointContent[pid]
			if content == "" {
				content = "（材料缺失）"
			}
			fmt.Fprintf(&sb, "  [%s] %s\n", pid, content)
		}
		sb.WriteString("\n")
	}

	raw, err := s.llmClient.CompleteJSON(ctx, "wiki_claim_verify.md",
		map[string]string{"claims_with_material": sb.String()}, "reasoning")
	if err != nil {
		slog.Warn("wiki: claim verify llm call failed, skipping verification", "page_id", pageID, "error", err)
		return
	}

	var output struct {
		Results []struct {
			ClaimID string `json:"claim_id"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		slog.Warn("wiki: claim verify parse failed, skipping verification", "page_id", pageID, "error", err)
		return
	}

	type verdictInfo struct{ verdict, reason string }
	byID := make(map[string]verdictInfo, len(output.Results))
	for _, r := range output.Results {
		v := r.Verdict
		if v != VerdictSupported && v != VerdictPartial && v != VerdictUnsupported {
			slog.Warn("wiki: claim verify returned unknown verdict, treating as partial",
				"page_id", pageID, "claim_id", r.ClaimID, "verdict", v)
			v = VerdictPartial
		}
		byID[r.ClaimID] = verdictInfo{verdict: v, reason: r.Reason}
	}

	for _, ref := range refs {
		info, ok := byID[ref.id]
		if !ok {
			// The model dropped this claim from its response — treat as
			// unverified rather than silently passing it: err toward
			// requiring a human look, not toward a false "supported".
			slog.Warn("wiki: claim verify response missing a claim, treating as partial", "page_id", pageID, "claim_id", ref.id)
			info = verdictInfo{verdict: VerdictPartial, reason: "核验响应缺失该结论"}
		}
		citedJSON, _ := json.Marshal(ref.claim.CitedPointIDs)
		if err := s.store.InsertClaimCheck(&ClaimCheck{
			PageID:        pageID,
			RevisionID:    revisionID,
			ClaimID:       ref.id,
			ClaimText:     ref.claim.Summary,
			CitedPointIDs: string(citedJSON),
			Verdict:       info.verdict,
			Reason:        info.reason,
		}); err != nil {
			slog.Error("wiki: insert claim check failed", "page_id", pageID, "claim_id", ref.id, "error", err)
		}
	}
}

// sentenceSplitRe splits a section of Markdown body text into rough
// sentences for uncitedSentenceRate — Chinese/English sentence-ending
// punctuation or a line break.
var sentenceSplitRe = regexp.MustCompile(`[。！？.!?\n]+`)

// uncitedSentenceRate implements docs/impl/v1/wiki-generation.md 阶段 G's
// uncited_sentence_rate metric: the share of prose sentences in "稳定结论" +
// "展开说明" (everything up to "待验证点") carrying no [point_id] tag.
// Markdown heading lines and empty fragments are excluded from the
// denominator — they aren't prose claims that should be cited.
func uncitedSentenceRate(content string) float64 {
	start := strings.Index(content, "## 稳定结论")
	if start < 0 {
		return 0
	}
	end := strings.Index(content, "## 待验证点")
	if end < 0 || end < start {
		end = len(content)
	}
	section := content[start:end]

	var sentences []string
	for _, p := range sentenceSplitRe.Split(section, -1) {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		sentences = append(sentences, p)
	}
	if len(sentences) == 0 {
		return 0
	}
	uncited := 0
	for _, s := range sentences {
		if !pointIDTagRe.MatchString(s) {
			uncited++
		}
	}
	return float64(uncited) / float64(len(sentences))
}

func conceptIDOf(page *Page) string {
	if page.EntryID.Valid {
		return page.EntryID.String
	}
	return ""
}

// Selfcheck implements docs/impl/v1/wiki-generation.md 阶段 G: replay real
// confident questions this page's qualifying KPs were once answered from
// against the compiled page itself, reusing the exact same answerFromPage
// path TryDirectAnswer uses — this is an external, ground-truthed check
// ("这批知识点当初是被慢路径以 confident 答对过的"), not the page grading
// itself. Results are cached per (page_id, revision_id): calling this twice
// for the same unchanged revision replays nothing the second time
// (docs/impl/v1/wiki-generation.md 阶段 G "与 publish 的关系").
func (s *Service) Selfcheck(ctx context.Context, pageID string) (*QualityCheck, error) {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}

	revisionID, err := s.store.LatestRevisionID(pageID)
	if err != nil {
		return nil, err
	}

	if cached, err := s.store.LatestQualityCheck(pageID, revisionID); err != nil {
		slog.Warn("wiki: lookup cached quality check failed, recomputing", "page_id", pageID, "error", err)
	} else if cached != nil {
		return cached, nil
	}

	var sourcePointIDs []string
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)

	replayN := s.cfg.SelfcheckReplayN
	if replayN <= 0 {
		replayN = 5
	}
	questions, err := s.store.ConfidentQuestionsForPoints(sourcePointIDs, replayN)
	if err != nil {
		slog.Warn("wiki: selfcheck fetch confident questions failed", "page_id", pageID, "error", err)
	}

	metrics := QualityMetrics{ReplaySampleSize: len(questions)}
	sufficientCount := 0
	for _, q := range questions {
		result, ok, answerErr := s.answerFromPage(ctx, q, page)
		if answerErr != nil {
			slog.Warn("wiki: selfcheck replay call failed", "page_id", pageID, "question", q, "error", answerErr)
			metrics.FailedQuestions = append(metrics.FailedQuestions, q)
			continue
		}
		if ok && result != nil {
			sufficientCount++
		} else {
			metrics.FailedQuestions = append(metrics.FailedQuestions, q)
		}
	}
	metrics.ReplaySufficientCount = sufficientCount
	if len(questions) > 0 {
		metrics.ReplaySufficientRate = float64(sufficientCount) / float64(len(questions))
	} else {
		// No real confident questions to replay yet (e.g. a very freshly
		// qualifying page) — don't block on an axis with no evidence either
		// way (docs/impl/v1/wiki-generation.md 阶段 G).
		metrics.ReplaySufficientRate = 1.0
	}

	// verified is no longer a qualifying-material gate (docs/design/
	// wiki-single-tier-revision.md: a human picking the entry_id set is
	// itself the admission signal) — always false now, regardless of origin.
	qualifying, err := s.store.ListQualifyingPoints(conceptIDOf(page), false)
	if err != nil {
		slog.Warn("wiki: selfcheck list qualifying points failed", "page_id", pageID, "error", err)
	}
	if len(qualifying) > 0 {
		metrics.MaterialUsageRate = float64(len(sourcePointIDs)) / float64(len(qualifying))
	} else {
		metrics.MaterialUsageRate = 1.0
	}

	metrics.UncitedSentenceRate = uncitedSentenceRate(page.Content)

	unsupported, err := s.store.UnsupportedClaimCount(pageID, revisionID)
	if err != nil {
		slog.Warn("wiki: selfcheck unsupported claim count failed", "page_id", pageID, "error", err)
	}
	metrics.UnsupportedClaimCount = unsupported

	minSufficient := s.cfg.SelfcheckMinSufficientRate
	if minSufficient <= 0 {
		minSufficient = 0.6
	}
	minMaterial := s.cfg.SelfcheckMinMaterialUsage
	if minMaterial <= 0 {
		minMaterial = 0.5
	}
	maxUncited := s.cfg.SelfcheckMaxUncitedRate
	if maxUncited <= 0 {
		maxUncited = 0.3
	}

	var reasons []string
	if metrics.ReplaySufficientRate < minSufficient {
		reasons = append(reasons, fmt.Sprintf("回放 sufficient 率 %.2f 低于门槛 %.2f", metrics.ReplaySufficientRate, minSufficient))
	}
	if metrics.MaterialUsageRate < minMaterial {
		reasons = append(reasons, fmt.Sprintf("材料利用率 %.2f 低于门槛 %.2f", metrics.MaterialUsageRate, minMaterial))
	}
	if metrics.UncitedSentenceRate > maxUncited {
		reasons = append(reasons, fmt.Sprintf("无引用句比例 %.2f 高于门槛 %.2f", metrics.UncitedSentenceRate, maxUncited))
	}
	if metrics.UnsupportedClaimCount > 0 {
		reasons = append(reasons, fmt.Sprintf("存在 %d 条结论未通过支持度核验", metrics.UnsupportedClaimCount))
	}
	metrics.BlockingReasons = reasons

	metricsJSON, err := json.Marshal(metrics)
	if err != nil {
		slog.Warn("wiki: marshal quality metrics failed, storing empty", "page_id", pageID, "error", err)
		metricsJSON = []byte("{}")
	}
	qc := &QualityCheck{
		PageID:     pageID,
		RevisionID: revisionID,
		Metrics:    string(metricsJSON),
		Passed:     len(reasons) == 0,
	}
	if err := s.store.InsertQualityCheck(qc); err != nil {
		slog.Error("wiki: insert quality check failed", "page_id", pageID, "error", err)
	}
	return qc, nil
}

// Publish implements docs/impl/v1/wiki.md 步骤 4 + docs/impl/v1/
// wiki-generation.md 阶段 G: POST /wiki/pages/:id/publish. Only valid from
// draft or needs_recompile. Kept as the zero-arg, force=false convenience
// form used throughout the existing call sites; PublishWithForce is the
// force-aware form the HTTP handler uses.
func (s *Service) Publish(pageID string) (*Page, error) {
	return s.publish(context.Background(), pageID, false)
}

// PublishWithForce implements the force=true branch of docs/impl/v1/
// wiki-generation.md 阶段 G "与 publish 的关系": a human can publish despite
// a failed quality gate, but that's a deliberate, logged override, not the
// default path — the gate result is still computed and persisted
// (QualityCheck.Forced=true) either way.
func (s *Service) PublishWithForce(ctx context.Context, pageID string, force bool) (*Page, error) {
	return s.publish(ctx, pageID, force)
}

func (s *Service) publish(ctx context.Context, pageID string, force bool) (*Page, error) {
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

	if s.cfg.SelfcheckEnabled {
		qc, err := s.Selfcheck(ctx, pageID)
		if err != nil {
			slog.Warn("wiki: selfcheck failed, proceeding without gate", "page_id", pageID, "error", err)
		} else if !qc.Passed {
			if !force {
				m, _ := qc.DecodeMetrics()
				return nil, fmt.Errorf("%w: %s", ErrQualityGateFailed, strings.Join(m.BlockingReasons, "; "))
			}
			// force=true: publish anyway, but flip the check record to a
			// forced override so it's distinguishable from an actual pass
			// (docs/impl/v1/wiki-generation.md 阶段 G).
			if err := s.store.MarkQualityCheckForced(qc.QCID); err != nil {
				slog.Error("wiki: record forced publish override failed", "page_id", pageID, "error", err)
			}
			slog.Warn("wiki: publishing despite failed quality gate (force=true)", "page_id", pageID)
		}
	}

	if err := s.store.PublishPage(pageID); err != nil {
		return nil, err
	}
	// 发布不改内容，只是状态跳变，但合并后的修订记录列表（用户 2026-08-19
	// 要求：写作草稿和修订记录合并为一份，动作要能看到"编译/草稿修订/发布"）
	// 需要一条真实的 reason=publish 行才能在列表里体现"这次发布"这件事，不
	// 是从别处拼出来的虚拟行。content/title 原样复制当前页面值（没有变化，
	// 只是记一笔"这一刻发布了"）。
	if err := s.store.InsertRevision(&Revision{PageID: pageID, Content: page.Content, Title: page.Title, Reason: "publish"}); err != nil {
		slog.Error("wiki: insert publish revision failed", "page_id", pageID, "error", err)
	}
	if err := s.indexPage(page); err != nil {
		slog.Error("wiki: index page after publish failed", "page_id", pageID, "error", err)
	}

	if err := s.RecomputeRelationsForPage(pageID); err != nil {
		slog.Error("wiki: recompute relations after publish failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: published page", "page_id", pageID, "entry_id", page.EntryID.String)
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

	slog.Info("wiki: archived page", "page_id", pageID)
	return s.store.GetPage(pageID)
}

// GetPage is a thin read-only accessor Retrieval's synthesis-audit-trial
// orchestration (docs/impl/v1/wiki.md 步骤 4a) uses to read a served page's
// source_point_ids for the independent-verification comparison — retrieval
// already imports wiki (it calls TryDirectAnswer), so this avoids exposing
// the whole *Store just for one field read.
func (s *Service) GetPage(pageID string) (*Page, error) {
	return s.store.GetPage(pageID)
}

// RecordSynthesisOutcome updates a page's synthesis-satisfaction axis
// (docs/impl/v1/wiki.md 步骤 4a) — the consumption-side counterpart of
// trace.Service's new WriteSynthesisOutcome, wired in via a small
// trace-package-local interface (analogous to Phase 4's
// retrieval.AuditOutcomeWriter) so trace doesn't need to import wiki.
// Deliberately a thin pass-through: the axis is observation-only and must
// never touch status/needs_recompile/index (docs/impl/v1/wiki.md 步骤 4a
// 「mean(page) 的消费方式」), so there is nothing else for this method to do.
func (s *Service) RecordSynthesisOutcome(pageID string, agree bool) error {
	return s.store.RecordSynthesisOutcome(pageID, agree)
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

// GetActivePageByEntryID exposes the store lookup for callers outside this
// package (docs/impl/v1/concept-evolution.md 步骤 3 merge 执行: find the page
// tied to a concept being merged so it can be flagged needs_recompile).
func (s *Service) GetActivePageByEntryID(conceptID string) (*Page, error) {
	return s.store.GetActivePageByEntryID(conceptID)
}

// NotifyEntriesChanged implements unit.WikiEntryNotifier/entry package's
// equivalent notification point (docs/impl/v1/wiki.md「重编译标记」,
// 2026-08-18 单层化收尾重新接线): entryIDs is the set of entry_id whose Core
// KP composition just changed (a KU's lifecycle transition, or a KU getting
// classified into/out of an entry). For each one, every *published* page
// compiled from it (wiki_page_entries — Core membership only, no Context/
// Conflict one-hop expansion) gets flagged needs_recompile via
// MarkNeedsRecompile, which itself no-ops on archived/already-needs_recompile
// pages; draft pages are skipped here since they were never live and have
// nothing stale to protect readers from. Best-effort per page — one failed
// lookup/mark doesn't stop the rest — errors are collected and returned
// joined so a caller can log/observe without this call ever partially
// hanging.
func (s *Service) NotifyEntriesChanged(entryIDs []string, reason string) error {
	seenEntry := make(map[string]bool, len(entryIDs))
	seenPage := make(map[string]bool)
	var errs []error
	for _, entryID := range entryIDs {
		if entryID == "" || seenEntry[entryID] {
			continue
		}
		seenEntry[entryID] = true

		pageIDs, err := s.store.PageIDsByEntryID(entryID)
		if err != nil {
			errs = append(errs, fmt.Errorf("wiki: notify entries changed: page ids by entry %s: %w", entryID, err))
			continue
		}
		for _, pageID := range pageIDs {
			if seenPage[pageID] {
				continue
			}
			seenPage[pageID] = true

			page, err := s.store.GetPage(pageID)
			if err != nil {
				errs = append(errs, fmt.Errorf("wiki: notify entries changed: get page %s: %w", pageID, err))
				continue
			}
			if page == nil || page.Status != StatusPublished {
				continue
			}
			if err := s.MarkNeedsRecompile(pageID, reason); err != nil {
				errs = append(errs, fmt.Errorf("wiki: notify entries changed: mark needs_recompile %s: %w", pageID, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
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
func (s *Service) TryDirectAnswer(ctx context.Context, question string, domainIDs []string, minScore float64, maxCandidates int) (*DirectAnswerResult, bool, error) {
	if maxCandidates <= 0 {
		maxCandidates = 3
	}

	candidates, err := s.gatherDirectAnswerCandidates(ctx, question, domainIDs, minScore, maxCandidates)
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

// gatherDirectAnswerCandidates implements docs/impl/v1/wiki-single-tier-
// task-brief.md 步骤 4's three direct-answer entries, merged and deduped, in
// priority order: Concept/Fact recognition hits (an LLM judgment against
// already-published entries — the most semantically targeted signal) first,
// then lexical hits (wiki index, including aliases/trigger_questions fields,
// score >= minScore) ordered by score, then concept-name hits not already
// present, truncated to maxCandidates.
//
// Single-tier (docs/impl/v1/wiki-single-tier-task-brief.md 步骤 2/3): Wiki no
// longer has a separate "topic page aggregates concept-page members" tier, so
// there is no more topic-page expansion step here — every hit is itself a
// directly answerable page.
func (s *Service) gatherDirectAnswerCandidates(ctx context.Context, question string, domainIDs []string, minScore float64, maxCandidates int) ([]string, error) {
	seen := make(map[string]bool)
	var candidates []string

	recognizedHits, err := s.matchEntriesByConceptRecognition(ctx, question, domainIDs)
	if err != nil {
		slog.Warn("wiki: concept/fact recognition lookup failed, continuing without it", "error", err)
	}
	for _, pageID := range recognizedHits {
		if seen[pageID] {
			continue
		}
		seen[pageID] = true
		candidates = append(candidates, pageID)
	}

	q := bleve.NewMatchQuery(question)
	req := bleve.NewSearchRequest(q)
	req.Size = maxCandidates

	results, err := s.wikiIndex.Search(req)
	if err != nil {
		return nil, fmt.Errorf("wiki: search: %w", err)
	}

	for _, hit := range results.Hits {
		if hit.Score < minScore || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		candidates = append(candidates, hit.ID)
	}

	conceptHits, err := s.matchEntryRow(question)
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

// renderEntryCandidateList formats entries into wiki_entry_recognize.md's
// entry_list text, mirroring unit.renderEntryList's shape (name/description/
// boundary — unit_entry_match.md's own disambiguation fields) so the two
// prompts read the same way despite matching different things (a knowledge
// unit vs. a question).
func renderEntryCandidateList(entries []EntryCandidate) string {
	var list strings.Builder
	for _, e := range entries {
		line := fmt.Sprintf("[%s] %s：%s", e.EntryID, e.Name, e.Description)
		if e.Boundary != "" {
			line += fmt.Sprintf("｜边界：%s", e.Boundary)
		}
		list.WriteString(line + "\n")
	}
	return list.String()
}

// RecognizeEntries is the shared LLM entry-matching core (config/prompts/
// wiki_entry_recognize.md): given free text (a retrieval question, or —
// 2026-08-19 新增复用 — a human-entered Wiki 生成主题名称/范围描述), judge
// which already-existing Concept/Fact entries it's mainly about, from the
// domain-scoped candidate list (domainIDs empty means every domain). Exported
// so callers outside this package's retrieval path (the Wiki 生成弹窗的词条
// 匹配, POST /wiki/entries/recognize) can reuse the exact same model call
// instead of a separate ad-hoc matching heuristic — the design decision is
// entry matching is always model-mediated, not just at answer time.
// Returns raw entry_ids (filtered to known candidates only); unlike
// matchEntriesByConceptRecognition it does NOT require the entry to already
// have a published page — the Wiki 生成弹窗 needs unpublished entries too
// (that's the whole point of picking them to compile).
func (s *Service) RecognizeEntries(ctx context.Context, text string, domainIDs []string) ([]string, error) {
	entries, err := s.store.ListEntriesForRecognition(domainIDs)
	if err != nil {
		return nil, fmt.Errorf("wiki: list entries for recognition: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.EntryID] = true
	}

	vars := map[string]string{
		"question":   text,
		"entry_list": renderEntryCandidateList(entries),
	}
	data, err := s.llmClient.CompleteJSON(ctx, "wiki_entry_recognize.md", vars, "extraction")
	if err != nil {
		return nil, fmt.Errorf("wiki: entry recognize llm call: %w", err)
	}

	var output struct {
		EntryIDs []string `json:"entry_ids"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("wiki: entry recognize parse: %w", err)
	}

	seen := make(map[string]bool, len(output.EntryIDs))
	var entryIDs []string
	for _, entryID := range output.EntryIDs {
		if entryID == "" || !known[entryID] || seen[entryID] {
			continue
		}
		seen[entryID] = true
		entryIDs = append(entryIDs, entryID)
	}
	return entryIDs, nil
}

// FilterPointCandidate is one candidate KP passed to FilterPoints — the
// caller (Wiki 生成弹窗，词条勾选完之后展开 KP 那一屏) already has the
// content in hand (fetched via GET /entries/:id for display), so this takes
// point_id+content directly instead of re-querying the DB.
type FilterPointCandidate struct {
	PointID string `json:"point_id"`
	Content string `json:"content"`
}

// FilterPoints (config/prompts/wiki_kp_filter.md, 2026-08-19 新增) is one LLM
// call judging which of the given candidate KP are actually relevant to a
// Wiki topic — a matched entry does not mean every KP it owns belongs in the
// topic's material, so this narrows within an already-matched entry set
// rather than matching entries themselves (that's RecognizeEntries' job).
// Returns the relevant subset of point_ids (filtered to known candidates
// only); the caller pre-checks these in the KP picker UI and, if the human
// confirms without further edits, the checked set becomes CompileRequest/
// AnalyzeRequest.PointIDs, restricting Core (buildKnowledgeSubgraph) to them.
// This is advisory-only at this layer — nothing forces the caller to use the
// result as anything more than a pre-check default.
func (s *Service) FilterPoints(ctx context.Context, topicText string, candidates []FilterPointCandidate) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	known := make(map[string]bool, len(candidates))
	var list strings.Builder
	for _, c := range candidates {
		if c.PointID == "" {
			continue
		}
		known[c.PointID] = true
		list.WriteString(fmt.Sprintf("[%s] %s\n", c.PointID, c.Content))
	}
	if len(known) == 0 {
		return nil, nil
	}

	vars := map[string]string{
		"topic_text": topicText,
		"point_list": list.String(),
	}
	data, err := s.llmClient.CompleteJSON(ctx, "wiki_kp_filter.md", vars, "extraction")
	if err != nil {
		return nil, fmt.Errorf("wiki: kp filter llm call: %w", err)
	}

	var output struct {
		PointIDs []string `json:"point_ids"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("wiki: kp filter parse: %w", err)
	}

	seen := make(map[string]bool, len(output.PointIDs))
	var pointIDs []string
	for _, id := range output.PointIDs {
		if id == "" || !known[id] || seen[id] {
			continue
		}
		seen[id] = true
		pointIDs = append(pointIDs, id)
	}
	return pointIDs, nil
}

// matchEntriesByConceptRecognition implements docs/impl/v1/wiki-single-tier-
// task-brief.md 步骤 4: one LLM call judging which already-existing Concept/
// Fact entries the question is mainly about (config/prompts/
// wiki_entry_recognize.md, entry_list rendered the same way
// unit_entry_match.md's is — see renderEntryCandidateList), replacing the
// old four-tuple exact-match entry (matchFourTupleEntry, removed). domainIDs
// scopes the candidate entry list the same way the rest of Retrieval's
// domain pre-filter does (retrieval.QueryContext.DomainIDs); empty means
// unresolved domain, so every entry across every domain is a candidate
// (mirrors unit.Store.GetEntriesByDomainID("")'s same fallback). No-ops
// (skips the LLM call entirely) when there are zero candidate entries to
// begin with — an empty domain shouldn't waste a call on a list with nothing
// to match, same reasoning as unit.Service.matchConceptBatches's empty-list
// guard. A recognized entry_id only becomes a candidate page if it has a
// currently *published* page (checked via Store.PageIDsByEntryID + GetPage);
// an entry with no page yet, or only a draft/archived one, is silently
// dropped rather than erroring — the caller is looking for existing
// answerable material, not entry existence.
func (s *Service) matchEntriesByConceptRecognition(ctx context.Context, question string, domainIDs []string) ([]string, error) {
	entryIDs, err := s.RecognizeEntries(ctx, question, domainIDs)
	if err != nil {
		return nil, err
	}
	if len(entryIDs) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	var pageIDs []string
	for _, entryID := range entryIDs {
		candidatePageIDs, err := s.store.PageIDsByEntryID(entryID)
		if err != nil {
			slog.Warn("wiki: page ids by entry id failed", "entry_id", entryID, "error", err)
			continue
		}
		for _, pageID := range candidatePageIDs {
			if seen[pageID] {
				continue
			}
			page, err := s.store.GetPage(pageID)
			if err != nil {
				slog.Warn("wiki: get page failed during entry recognition", "page_id", pageID, "error", err)
				continue
			}
			if page == nil || page.Status != StatusPublished {
				continue
			}
			seen[pageID] = true
			pageIDs = append(pageIDs, pageID)
		}
	}
	return pageIDs, nil
}

// matchEntryRow implements docs/impl/v1/wiki.md 步骤 4's concept入口:
// word-lexical containment (not embedding, not LLM) between the question and
// every published page's concept name, so a question mentioning the concept
// but not the page's wording (or the wiki index's aliases/trigger_questions)
// still finds the page.
func (s *Service) matchEntryRow(question string) ([]string, error) {
	pages, err := s.store.ListPublishedEntryPages()
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
		"entry_id":          page.EntryID.String,
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

// isManualCompiledFrom reports whether a page's compiled_from (its most
// recent compile/recompile provenance, docs/impl/v1/wiki.md 步骤 2) is the
// manual-trigger sentinel rather than a Study wiki_candidate result_id —
// used to carry the not-required-verified qualifying definition
// (2026-08-07 修订) across Recompile/Selfcheck for pages that were manually
// compiled.
func isManualCompiledFrom(compiledFrom string) bool {
	var ids []string
	if err := json.Unmarshal([]byte(compiledFrom), &ids); err != nil {
		return false
	}
	for _, id := range ids {
		if id == ManualTriggerSentinel {
			return true
		}
	}
	return false
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
