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
	"github.com/jxman78/wiki-brain/internal/foundation/graph"
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
	// takes already-published word pages as material, not KPs. This
	// KP-based one-tier path only ever produces concept or fact pages
	// (docs/impl/v1/wiki.md「概念页 / 事实页」，2026-08-03 修订).
	if req.PageType != "" && req.PageType != PageTypeConcept && req.PageType != PageTypeFact {
		return nil, fmt.Errorf("wiki: invalid page_type %q (topic pages are compiled via POST /wiki/pages/:id/topic/analyze|compile)", req.PageType)
	}

	// requireVerified: result_id present means Study already recommended this
	// concept (verified material is part of that judgment); empty means a
	// human triggered this directly, and qualifying drops the verified
	// requirement (docs/impl/v1/wiki.md 步骤 2 "人工指定主题手动编译"
	// 2026-08-07 修订).
	requireVerified := req.ResultID != ""
	in, err := s.gatherAnalyzeInputs(req.EntryID, requireVerified)
	if err != nil {
		return nil, err
	}
	// page_type is derived from the target entries row's kind, not freely
	// chosen by the caller (docs/impl/v1/wiki.md「数据结构」page_type 语义).
	// Callers that don't know the kind ahead of time (e.g. the manual-compile
	// UI picking a concept off a catalog card) can just omit page_type and
	// let it be derived; a caller that does supply one is still held to it
	// matching, as defense against a stale/wrong value.
	want := pageTypeForKind(in.conceptKind)
	if req.PageType == "" {
		req.PageType = want
	} else if req.PageType != want {
		return nil, fmt.Errorf("wiki: page_type %q does not match concept %s's kind (expected %q)", req.PageType, req.EntryID, want)
	}
	readiness := s.computeReadiness(in)

	claims, tensions, err := s.analyzeClaimsWithInputs(ctx, req.EntryID, in)
	if err != nil {
		return nil, err
	}
	return &AnalyzeResult{
		EntryID:   req.EntryID,
		PageType:  req.PageType,
		ResultID:  req.ResultID,
		Claims:    claims,
		Tensions:  tensions,
		Readiness: readiness,
	}, nil
}

// computeReadiness builds an informational-only snapshot mirroring Study's
// own wiki-candidate "ready" criteria (docs/design/wiki-compilation.md
// "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格立传'")
// so a human manually picking a concept to compile (docs/impl/v1/wiki.md 步骤
// 2 "人工指定主题手动编译") can see the same signals Study would use before
// deciding a concept is "ready", without waiting for Study to flag it as a
// wiki_candidate first. This never gates Analyze/Compile — purely additive,
// same non-breaking precedent as Claim.AspectID.
//
// Deliberately NOT expected to be byte-identical to Study's own
// Stats.Cohesion: Study's cohesion graph only uses KPN relations +
// cooccurrence (study/store.go PairSignals), while this reuses wiki's own
// aspect-clustering communities (aspect.go's buildAspects), which
// additionally fold in intent/unit signals and go through split/merge
// post-processing. Both are legitimate cohesion estimates for different
// purposes (Study gates on it; this only informs a human) — the two numbers
// may reasonably differ slightly, and that's fine since neither number here
// blocks anything.
func (s *Service) computeReadiness(in *analyzeInputs) *Readiness {
	pointIDs := make([]string, len(in.qualifying))
	for i, p := range in.qualifying {
		pointIDs[i] = p.PointID
	}

	related, contradicts, err := s.store.KPNConnectionCountsByType(pointIDs)
	if err != nil {
		slog.Warn("wiki: readiness kpn connection counts failed, continuing without them", "error", err)
	}
	daysActive, err := s.store.DaysActive(pointIDs)
	if err != nil {
		slog.Warn("wiki: readiness days active failed, continuing without it", "error", err)
	}

	partition := make([][]string, len(in.aspects))
	for i, a := range in.aspects {
		partition[i] = a.PointIDs
	}
	cohesion := graph.LargestShare(partition)

	return &Readiness{
		QualifyingKPCount:          len(in.qualifying),
		RelatedConnectionCount:     related,
		ContradictsConnectionCount: contradicts,
		DaysActive:                 daysActive,
		DaysActiveMin:              s.cfg.QualifyingMinDaysActive,
		Cohesion:                   cohesion,
		CohesionMin:                s.cfg.EntryCohesionMin,
	}
}

// Compile implements docs/impl/v1/wiki.md 步骤 2-3: POST /wiki/compile.
func (s *Service) Compile(ctx context.Context, req CompileRequest) (*Page, error) {
	if req.PageType != "" && req.PageType != PageTypeConcept && req.PageType != PageTypeFact {
		return nil, fmt.Errorf("wiki: invalid page_type %q (topic pages are compiled via POST /wiki/pages/:id/topic/analyze|compile)", req.PageType)
	}
	_, _, _, conceptKind, err := s.store.GetEntryInfo(req.EntryID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get concept info: %w", err)
	}
	// See Analyze's identical comment: page_type is derived when omitted,
	// validated when supplied.
	want := pageTypeForKind(conceptKind)
	if req.PageType == "" {
		req.PageType = want
	} else if req.PageType != want {
		return nil, fmt.Errorf("wiki: page_type %q does not match concept %s's kind (expected %q)", req.PageType, req.EntryID, want)
	}

	if req.ResultID != "" {
		if s.activationSvc != nil {
			if err := s.activationSvc.Store().ResolvePending(req.ResultID, activation.ResultApplied, "manual"); err != nil {
				slog.Warn("wiki: resolve pending wiki_candidate result failed", "result_id", req.ResultID, "error", err)
			}
		}
	} else {
		slog.Info("wiki: compile triggered without result_id (manual trigger, docs/impl/v1/wiki.md 步骤 2)", "entry_id", req.EntryID)
	}

	if existing, err := s.store.GetActivePageByEntryID(req.EntryID); err != nil {
		return nil, fmt.Errorf("wiki: check existing page: %w", err)
	} else if existing != nil {
		return nil, ErrPageAlreadyExists
	}

	// requireVerified: see the identical comment in Analyze — result_id
	// present means Study already recommended this concept on verified
	// material; empty means a human triggered this directly, and qualifying
	// drops the verified requirement.
	requireVerified := req.ResultID != ""

	claims, tensions := req.Claims, req.Tensions
	if len(claims) == 0 {
		// No analysis round-tripped back (caller skipped /wiki/compile/analyze
		// entirely — fine for either the Study-recommended or manual-trigger
		// path) — run it internally so generation is still constrained to an
		// analysis result, not raw material access.
		var err error
		claims, tensions, err = s.analyzeClaims(ctx, req.EntryID, requireVerified)
		if err != nil {
			return nil, err
		}
	}

	compiled, err := s.compileContent(ctx, req.EntryID, req.PageType, claims, tensions, requireVerified)
	if err != nil {
		return nil, err
	}

	compiledFromIDs := nonEmpty(req.ResultID)
	if len(compiledFromIDs) == 0 {
		// No Study wiki_candidate result_id round-tripped back — a human
		// picked this concept directly (docs/impl/v1/wiki.md 步骤 2 "人工指定
		// 主题手动编译"), not the "debug path" this used to exclusively mean.
		// Recorded so pages can later be told apart by origin without a new
		// column (see ManualTriggerSentinel).
		compiledFromIDs = []string{ManualTriggerSentinel}
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
		CompiledFrom:       marshalIDs(compiledFromIDs),
		Summary:            compiled.summary,
		Aspects:            compiled.aspectsJSON,
		PromptVersion:      "v1",
		ModelName:          "reasoning",
	}
	page.EntryID = nullableString(req.EntryID)

	if err := s.store.InsertPage(page); err != nil {
		return nil, err
	}
	rev := &Revision{PageID: page.PageID, Content: page.Content, Reason: "compile"}
	if err := s.store.InsertRevision(rev); err != nil {
		slog.Error("wiki: insert initial revision failed", "page_id", page.PageID, "error", err)
	} else {
		s.verifyClaims(ctx, page.PageID, rev.RevisionID, req.EntryID, claims, requireVerified)
	}

	slog.Info("wiki: compiled draft page", "page_id", page.PageID, "entry_id", req.EntryID,
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
	if page.EntryID.Valid {
		conceptID = page.EntryID.String
	}

	// requireVerified follows the page's own original origin (docs/impl/v1/
	// wiki.md 步骤 2 2026-08-07 修订) — a page first compiled manually stays
	// on the not-required-verified qualifying definition across recompiles;
	// a Study-recommended page keeps requiring verified.
	requireVerified := !isManualCompiledFrom(page.CompiledFrom)

	// Recompile has no exposed analyze-preview step (docs/impl/v1/wiki.md
	// 步骤 5): the human confirming "recompile" on the Page is itself the
	// confirmation of this new analysis round. Always a full page regeneration
	// (阶段 A→D rerun) — no per-section incremental diffing (docs/impl/v1/
	// wiki-generation.md 第 8 节, 明确不做).
	claims, tensions, err := s.analyzeClaims(ctx, conceptID, requireVerified)
	if err != nil {
		return nil, err
	}
	compiled, err := s.compileContent(ctx, conceptID, page.PageType, claims, tensions, requireVerified)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(pageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalConditions(compiled.observedConditions),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalUncoveredPoints(compiled.uncoveredPoints),
		marshalIDs(compiledFrom), compiled.summary, compiled.aspectsJSON, "v1", "reasoning"); err != nil {
		return nil, err
	}
	rev := &Revision{PageID: pageID, Content: compiled.content, Reason: reason}
	if err := s.store.InsertRevision(rev); err != nil {
		slog.Error("wiki: insert recompile revision failed", "page_id", pageID, "error", err)
	} else {
		s.verifyClaims(ctx, pageID, rev.RevisionID, conceptID, claims, requireVerified)
	}

	// A page must not answer directly while it's being recompiled/awaiting
	// re-publish under a stale index entry.
	if err := s.wikiIndex.Delete(pageID); err != nil {
		slog.Warn("wiki: remove page from index after recompile failed", "page_id", pageID, "error", err)
	}

	slog.Info("wiki: recompiled page", "page_id", pageID, "reason", reason)
	return s.store.GetPage(pageID)
}

// compiledContent is compileContent's output — one fully-assembled page
// generated in a single LLM call (docs/impl/v1/wiki-generation.md 阶段 D/F).
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
	aspectsJSON        string
}

// entryKindLabel returns the Chinese page-kind label injected as
// {{entry_kind_label}} in wiki_analyze / wiki_compile
// (docs/impl/v1/wiki.md 步骤 3): 概念 or 事实. Unknown/empty kind defaults to
// 概念, matching ValidateEntryKind / pageTypeForKind.
func entryKindLabel(kind string) string {
	if kind == "fact" {
		return "事实"
	}
	return "概念"
}

// conceptKindHint renders the analyze/compile prompts' {{entry_kind_hint}}
// var from the concept's kind (docs/impl/v1/wiki.md 步骤 3 entry_kind_hint,
// docs/impl/v1/kpn.md 步骤 3 "类型标注"): a wording nudge only — it does not
// change page_type, the required section structure, or any qualifying/ready
// gate. Unknown/empty kind falls back to the concept-page hint, same as
// entry.ValidateEntryKind's own empty-defaults-to-concept behavior.
func conceptKindHint(kind string) string {
	switch kind {
	case "fact":
		return "这是一个事实页，围绕这个具体、可唯一指认的对象组织论断，claim 应回答'这是什么、当前状态如何、和哪些概念或其他事实存在关联'，不要泛化成脱离这个具体对象的通用规律。"
	default:
		return "这是一个概念页，围绕通用规律/原理/规则组织论断，claim 应回答'这个概念的定义、边界、内部逻辑是什么'，不要陷入某一个具体实现的细节。"
	}
}

// analyzeInputs bundles 阶段 A/B's material-gathering output for the
// analysis stage (docs/impl/v1/wiki-generation.md 3.1): qualifying KPs
// grouped by aspect.go's clustering instead of a flat list.
type analyzeInputs struct {
	conceptName    string
	conceptDesc    string
	conceptKind    string
	domainName     string
	qualifying     []QualifyingPoint
	aspects        []Aspect          // after budget truncation (whole aspects, not individual KPs)
	pointAspect    map[string]string // point_id -> aspect_id, for Claim.AspectID fallback correction
	whitelist      map[string]bool   // union of every kept aspect's point_ids
	aspectsText    string
	contradictions string
	gapsText       string
}

// gatherAnalyzeInputs clusters aspects, then truncates by whole aspects
// (largest first) to fit wiki.compile_max_chars — never by individual KP —
// so whatever survives still forms complete aspects (docs/impl/v1/
// wiki-generation.md 3.1).
func (s *Service) gatherAnalyzeInputs(conceptID string, requireVerified bool) (*analyzeInputs, error) {
	conceptName, conceptDesc, domainID, conceptKind, err := s.store.GetEntryInfo(conceptID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get concept info: %w", err)
	}
	domainName, err := s.store.GetDomainName(domainID)
	if err != nil {
		slog.Warn("wiki: get domain name failed, continuing without it", "entry_id", conceptID, "error", err)
	}

	qualifying, err := s.store.ListQualifyingPoints(conceptID, requireVerified)
	if err != nil {
		return nil, fmt.Errorf("wiki: list qualifying points: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, fmt.Errorf("%w: concept %s currently has no qualifying knowledge points (material may have just been replaced by a reupload and hasn't earned verified activation yet)", ErrNoQualifyingPoints, conceptID)
	}
	byID := make(map[string]QualifyingPoint, len(qualifying))
	for _, p := range qualifying {
		byID[p.PointID] = p
	}

	aspects, err := s.buildAspects(qualifying)
	if err != nil {
		return nil, fmt.Errorf("wiki: build aspects: %w", err)
	}

	maxChars := s.cfg.CompileMaxChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	kept := s.selectAspectsWithinBudget(aspects, byID, maxChars)

	pointAspect := make(map[string]string)
	whitelist := make(map[string]bool)
	for _, a := range kept {
		for _, pid := range a.PointIDs {
			pointAspect[pid] = a.AspectID
			whitelist[pid] = true
		}
	}

	aspectsMax := s.cfg.AspectQuestionsMax
	if aspectsMax <= 0 {
		aspectsMax = 5
	}
	aspectsText := s.renderAspectsText(kept, byID, aspectsMax)
	contradictionsText, err := s.renderContradictions(kept, whitelist)
	if err != nil {
		slog.Warn("wiki: render contradictions failed, continuing without them", "entry_id", conceptID, "error", err)
	}
	gapsText, err := s.matchingGaps(conceptName, qualifying)
	if err != nil {
		slog.Warn("wiki: gather gaps failed, continuing without them", "entry_id", conceptID, "error", err)
	}

	return &analyzeInputs{
		conceptName:    conceptName,
		conceptDesc:    conceptDesc,
		conceptKind:    conceptKind,
		domainName:     domainName,
		qualifying:     qualifying,
		aspects:        kept,
		pointAspect:    pointAspect,
		whitelist:      whitelist,
		aspectsText:    aspectsText,
		contradictions: contradictionsText,
		gapsText:       gapsText,
	}, nil
}

// selectAspectsWithinBudget implements 3.1's truncation rule: over budget,
// cut whole aspects — not individual KPs — largest (by KP count) first, so
// whatever survives is still structurally complete. Aspect order in the
// output preserves the input's aspect_id ordering (ClusterAspects already
// sorted it); only membership is filtered.
func (s *Service) selectAspectsWithinBudget(aspects []Aspect, byID map[string]QualifyingPoint, maxChars int) []Aspect {
	ranked := make([]Aspect, len(aspects))
	copy(ranked, aspects)
	sort.SliceStable(ranked, func(i, j int) bool { return len(ranked[i].PointIDs) > len(ranked[j].PointIDs) })

	keptIDs := make(map[string]bool, len(aspects))
	total := 0
	for _, a := range ranked {
		n := 0
		for _, pid := range a.PointIDs {
			n += len([]rune(byID[pid].Content))
		}
		if total+n > maxChars && len(keptIDs) > 0 {
			continue
		}
		keptIDs[a.AspectID] = true
		total += n
	}

	var out []Aspect
	for _, a := range aspects {
		if keptIDs[a.AspectID] {
			out = append(out, a)
		}
	}
	return out
}

// renderAspectsText formats the per-aspect material block for the analyze
// prompt's {{aspects}} var (3.1): suggested name, each point's content, and
// up to aspectsMax real confident questions this aspect's KPs were once
// answered from.
func (s *Service) renderAspectsText(aspects []Aspect, byID map[string]QualifyingPoint, aspectsMax int) string {
	var sb strings.Builder
	for _, a := range aspects {
		fmt.Fprintf(&sb, "【切面 %s】建议名称：%s\n", a.AspectID, a.SuggestedName)
		for _, pid := range a.PointIDs {
			p := byID[pid]
			fmt.Fprintf(&sb, "  [%s] %s\n", pid, p.Content)
		}
		questions, err := s.store.ConfidentQuestionsForPoints(a.PointIDs, aspectsMax)
		if err != nil {
			slog.Warn("wiki: fetch aspect confident questions failed, continuing without them", "aspect_id", a.AspectID, "error", err)
		}
		if len(questions) > 0 {
			sb.WriteString("  真实被问过的问题：\n")
			for _, q := range questions {
				fmt.Fprintf(&sb, "    - %s\n", q)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderContradictions formats cross-aspect contradicts pairs for the
// analyze prompt's {{contradictions}} var (3.1) — same-aspect contradicts
// are already inside that aspect's material (and, per 2.1, contradicts
// counts as a same-topic signal at clustering time); only pairs that landed
// in different aspects are worth surfacing here as a page-level tension.
func (s *Service) renderContradictions(aspects []Aspect, whitelist map[string]bool) (string, error) {
	pointIDs := make([]string, 0, len(whitelist))
	aspectOf := make(map[string]string, len(whitelist))
	for _, a := range aspects {
		for _, pid := range a.PointIDs {
			pointIDs = append(pointIDs, pid)
			aspectOf[pid] = a.AspectID
		}
	}
	rels, err := s.store.RelationsAmong(pointIDs)
	if err != nil {
		return "", fmt.Errorf("wiki: contradictions relations: %w", err)
	}

	var sb strings.Builder
	for _, r := range rels {
		if r.RelationType != "contradicts" {
			continue
		}
		aa, ab := aspectOf[r.SourcePointID], aspectOf[r.TargetPointID]
		if aa == "" || ab == "" || aa == ab {
			continue
		}
		fmt.Fprintf(&sb, "- [%s]（切面 %s） 与 [%s]（切面 %s） 矛盾\n", r.SourcePointID, aa, r.TargetPointID, ab)
	}
	return sb.String(), nil
}

// analyzeClaims implements docs/impl/v1/wiki-generation.md 阶段 C: call the
// analysis Prompt once (retrying once on validation failure) to get the
// proposed claim structure, validated against the aspect-scoped material
// whitelist, then correct each claim's optional AspectID against the
// aspect set it was actually validated against (3.4). Shared by Analyze,
// Compile (debug/no-analysis path) and Recompile.
func (s *Service) analyzeClaims(ctx context.Context, conceptID string, requireVerified bool) ([]Claim, []Tension, error) {
	in, err := s.gatherAnalyzeInputs(conceptID, requireVerified)
	if err != nil {
		return nil, nil, err
	}
	return s.analyzeClaimsWithInputs(ctx, conceptID, in)
}

// analyzeClaimsWithInputs is analyzeClaims split so Analyze() can reuse the
// same *analyzeInputs it already built for computeReadiness (docs/impl/v1/
// wiki.md 步骤 2 "人工指定主题手动编译") without deriving aspects/qualifying
// points twice.
func (s *Service) analyzeClaimsWithInputs(ctx context.Context, conceptID string, in *analyzeInputs) ([]Claim, []Tension, error) {
	vars := map[string]string{
		"entry_name":        in.conceptName,
		"entry_description": in.conceptDesc,
		"domain_name":       in.domainName,
		"aspects":           in.aspectsText,
		"contradictions":    in.contradictions,
		"gaps":              in.gapsText,
		"entry_kind_label":  entryKindLabel(in.conceptKind),
		"entry_kind_hint":   conceptKindHint(in.conceptKind),
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_analyze.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: analyze llm call failed", "attempt", attempt, "entry_id", conceptID, "error", err)
			continue
		}

		var output struct {
			Claims   []Claim   `json:"claims"`
			Tensions []Tension `json:"tensions"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: analyze parse failed", "attempt", attempt, "entry_id", conceptID, "error", err)
			continue
		}

		claims := filterClaims(output.Claims, in.whitelist, conceptID)
		tensions := filterTensions(output.Tensions, in.whitelist, conceptID)
		if len(claims) == 0 {
			lastErr = fmt.Errorf("wiki: analysis produced no usable claims")
			slog.Warn("wiki: analyze produced no usable claims", "attempt", attempt, "entry_id", conceptID)
			continue
		}

		correctAspectIDs(claims, in.aspects, in.pointAspect)
		return claims, tensions, nil
	}

	return nil, nil, fmt.Errorf("wiki: analyze failed after retry: %w", lastErr)
}

// correctAspectIDs implements 3.4's aspect_id fallback: any claim whose
// AspectID is empty or doesn't name a known aspect gets corrected from
// whichever aspect its cited_point_ids mostly belong to (ties or no match
// fall back to the reserved "misc" bucket). aspect_id never gates citation
// whitelisting — this only affects how compileContent groups "展开说明".
func correctAspectIDs(claims []Claim, aspects []Aspect, pointAspect map[string]string) {
	validIDs := make(map[string]bool, len(aspects))
	for _, a := range aspects {
		validIDs[a.AspectID] = true
	}
	for i := range claims {
		if validIDs[claims[i].AspectID] {
			continue
		}
		claims[i].AspectID = fallbackAspectID(claims[i].CitedPointIDs, pointAspect)
	}
}

func fallbackAspectID(citedPointIDs []string, pointAspect map[string]string) string {
	counts := make(map[string]int)
	for _, pid := range citedPointIDs {
		if a, ok := pointAspect[pid]; ok {
			counts[a]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	best, bestCount, tie := "", 0, false
	for _, k := range keys {
		switch {
		case counts[k] > bestCount:
			best, bestCount, tie = k, counts[k], false
		case counts[k] == bestCount && bestCount > 0:
			tie = true
		}
	}
	if best == "" || tie {
		return aspectMiscID
	}
	return best
}

// renderClaimsByAspect formats confirmed claims grouped by their (corrected)
// AspectID for the compile prompt's {{claims_by_aspect}} var (4.2) — the
// program-computed organization the compile prompt is told to preserve in
// "展开说明"'s per-aspect "###" subsections, rather than writing them from a
// flat claims list. Claims whose AspectID doesn't match a kept aspect (e.g.
// truncated out of budget at analysis time and never fixed by
// correctAspectIDs' fallback) land in a trailing "跨切面/未分类" group.
func renderClaimsByAspect(aspects []Aspect, byID map[string]QualifyingPoint, claims []Claim) string {
	byAspect := make(map[string][]Claim, len(aspects))
	var misc []Claim
	known := make(map[string]bool, len(aspects))
	for _, a := range aspects {
		known[a.AspectID] = true
	}
	for _, c := range claims {
		if known[c.AspectID] {
			byAspect[c.AspectID] = append(byAspect[c.AspectID], c)
		} else {
			misc = append(misc, c)
		}
	}

	var sb strings.Builder
	for _, a := range aspects {
		cs := byAspect[a.AspectID]
		if len(cs) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "【切面 %s】建议标题：%s\n", a.AspectID, a.SuggestedName)
		sb.WriteString("材料：\n")
		for _, pid := range a.PointIDs {
			p := byID[pid]
			fmt.Fprintf(&sb, "  [%s] %s\n", pid, p.Content)
		}
		sb.WriteString("该切面的稳定结论：\n")
		for _, c := range cs {
			fmt.Fprintf(&sb, "  - %s [%s]\n", c.Summary, strings.Join(c.CitedPointIDs, ", "))
		}
		sb.WriteString("\n")
	}
	if len(misc) > 0 {
		sb.WriteString("【跨切面/未分类】\n")
		for _, c := range misc {
			fmt.Fprintf(&sb, "  - %s [%s]\n", c.Summary, strings.Join(c.CitedPointIDs, ", "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
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

// compileContent implements docs/impl/v1/wiki-generation.md 阶段 D/F: given
// already-confirmed claims/tensions, call the generation Prompt once
// (retrying once on validation failure), organizing "展开说明" by the same
// aspect grouping analysis used, and validate the result. Shared by Compile
// and Recompile. Aspects are recomputed here rather than threaded through
// from analyzeClaims, since Compile also accepts a caller-supplied
// claims/tensions pair that may have skipped analyzeClaims entirely.
func (s *Service) compileContent(ctx context.Context, conceptID, pageType string, claims []Claim, tensions []Tension, requireVerified bool) (*compiledContent, error) {
	if len(claims) == 0 {
		return nil, fmt.Errorf("wiki: no confirmed claims for concept %s", conceptID)
	}

	qualifying, err := s.store.ListQualifyingPoints(conceptID, requireVerified)
	if err != nil {
		return nil, fmt.Errorf("wiki: list qualifying points: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, fmt.Errorf("%w: concept %s currently has no qualifying knowledge points (material may have just been replaced by a reupload and hasn't earned verified activation yet)", ErrNoQualifyingPoints, conceptID)
	}
	byID := make(map[string]QualifyingPoint, len(qualifying))
	for _, p := range qualifying {
		byID[p.PointID] = p
	}

	conceptName, conceptDesc, _, conceptKind, err := s.store.GetEntryInfo(conceptID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get concept info: %w", err)
	}

	aspects, err := s.buildAspects(qualifying)
	if err != nil {
		return nil, fmt.Errorf("wiki: build aspects: %w", err)
	}
	maxChars := s.cfg.CompileMaxChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	kept := s.selectAspectsWithinBudget(aspects, byID, maxChars)

	// Citation whitelist is every qualifying KP shown in the (budget-kept)
	// aspect material, same scope the analysis stage validated against — not
	// narrowed further to only what confirmed claims already cite, since the
	// compile prompt also sees the full aspect material and may legitimately
	// elaborate with additional supporting point_ids in "展开说明"
	// (docs/impl/v1/wiki-generation.md 4.3 "白名单范围不变").
	whitelist := make(map[string]bool)
	pointAspect := make(map[string]string)
	for _, a := range kept {
		for _, pid := range a.PointIDs {
			whitelist[pid] = true
			pointAspect[pid] = a.AspectID
		}
	}

	// Claims may arrive here without ever going through analyzeClaims'
	// correction (e.g. a human-edited round trip via CompileRequest.Claims) —
	// idempotent to reapply.
	correctAspectIDs(claims, kept, pointAspect)
	claimsByAspectText := renderClaimsByAspect(kept, byID, claims)

	tensionsJSON := "[]"
	if len(tensions) > 0 {
		if b, err := json.Marshal(tensions); err == nil {
			tensionsJSON = string(b)
		}
	}

	gapsText, err := s.matchingGaps(conceptName, qualifying)
	if err != nil {
		slog.Warn("wiki: gather gaps failed, continuing without them", "entry_id", conceptID, "error", err)
	}

	vars := map[string]string{
		"entry_name":        conceptName,
		"entry_description": conceptDesc,
		"claims_by_aspect":  claimsByAspectText,
		"tensions":          tensionsJSON,
		"gaps":              gapsText,
		"entry_kind_label":  entryKindLabel(conceptKind),
		"entry_kind_hint":   conceptKindHint(conceptKind),
	}

	triggerMax := s.cfg.TriggerQuestionsMax
	if triggerMax <= 0 {
		triggerMax = 10
	}

	// aliases/trigger_questions are not LLM output (docs/design/
	// wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组" 生成侧
	// 修订): both are retrieval metadata backed by data the system already
	// has verified, not knowledge expression — best-effort, failure just
	// means the page stores an empty list for that field.
	aliases, err := s.store.EntryAliases(conceptName)
	if err != nil {
		slog.Warn("wiki: fetch concept aliases failed, storing empty", "entry_id", conceptID, "error", err)
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		content, err := s.llmClient.Complete(ctx, "wiki_compile.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: compile llm call failed", "attempt", attempt, "entry_id", conceptID, "error", err)
			continue
		}

		filteredContent, citedInContent, stripped := filterContentTags(content, whitelist)
		if len(stripped) > 0 {
			slog.Warn("wiki: stripped out-of-whitelist point_id tags from content", "entry_id", conceptID, "ids", stripped)
		}

		if !hasRequiredSections(filteredContent) {
			lastErr = fmt.Errorf("wiki: compiled content missing required sections")
			slog.Warn("wiki: compile missing required sections", "attempt", attempt, "entry_id", conceptID)
			continue
		}
		if len(citedInContent) == 0 {
			lastErr = fmt.Errorf("wiki: compiled content has no whitelisted citations")
			slog.Warn("wiki: compile produced no usable citations", "attempt", attempt, "entry_id", conceptID)
			continue
		}

		sourceUnitIDs := sourceUnitsForPoints(citedInContent, qualifying)
		sourceLinkIDs, err := s.store.VerifiedLinkIDsForPoints(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup verified link ids for cited points failed", "entry_id", conceptID, "error", err)
		}
		observedConditions, err := s.store.VerifiedLinksObservedConditions(citedInContent)
		if err != nil {
			slog.Warn("wiki: lookup observed conditions for cited points failed", "entry_id", conceptID, "error", err)
		}
		triggerQuestions, err := s.store.ConfidentQuestionsForPoints(citedInContent, triggerMax)
		if err != nil {
			slog.Warn("wiki: fetch confident questions failed, storing empty", "entry_id", conceptID, "error", err)
		}
		uncoveredPoints, err := s.store.ListUncoveredPoints(conceptID)
		if err != nil {
			slog.Warn("wiki: list uncovered points failed, storing empty", "entry_id", conceptID, "error", err)
		}

		aspectQuestionsMax := s.cfg.AspectQuestionsMax
		if aspectQuestionsMax <= 0 {
			aspectQuestionsMax = 5
		}
		pageAspects := make([]PageAspect, 0, len(kept))
		for _, a := range kept {
			questionTypes, err := s.store.ConfidentQuestionsForPoints(a.PointIDs, aspectQuestionsMax)
			if err != nil {
				slog.Warn("wiki: fetch aspect question types failed, continuing without them", "aspect_id", a.AspectID, "error", err)
			}
			pageAspects = append(pageAspects, PageAspect{
				AspectID:      a.AspectID,
				Heading:       a.SuggestedName,
				PointIDs:      a.PointIDs,
				QuestionTypes: questionTypes,
			})
		}
		aspectsJSON, err := json.Marshal(pageAspects)
		if err != nil {
			slog.Warn("wiki: marshal page aspects failed, storing empty", "entry_id", conceptID, "error", err)
			aspectsJSON = []byte("[]")
		}

		if len(aliases) == 0 || len(triggerQuestions) == 0 {
			slog.Info("wiki: no programmatic aliases/trigger_questions available, storing empty",
				"entry_id", conceptID, "aliases", len(aliases), "trigger_questions", len(triggerQuestions))
		}

		return &compiledContent{
			title:              conceptName,
			content:            filteredContent,
			summary:            extractSection(filteredContent, "## 摘要"),
			sourcePointIDs:     citedInContent,
			sourceUnitIDs:      sourceUnitIDs,
			sourceLinkIDs:      sourceLinkIDs,
			observedConditions: observedConditions,
			aliases:            truncateStrings(aliases, triggerMax),
			triggerQuestions:   truncateStrings(triggerQuestions, triggerMax),
			uncoveredPoints:    uncoveredPoints,
			aspectsJSON:        string(aspectsJSON),
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
		slog.Warn("wiki: dropped out-of-whitelist cited_point_ids in analysis", "entry_id", conceptID, "ids", droppedIDs)
	}
	if droppedClaims > 0 {
		slog.Warn("wiki: dropped claims with no whitelisted citations after filtering", "entry_id", conceptID, "count", droppedClaims)
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
		slog.Warn("wiki: dropped out-of-whitelist related_point_ids in analysis", "entry_id", conceptID, "ids", dropped)
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
func (s *Service) verifyClaims(ctx context.Context, pageID, revisionID, conceptID string, claims []Claim, requireVerified bool) {
	if !s.cfg.ClaimVerifyEnabled || len(claims) == 0 {
		return
	}

	qualifying, err := s.store.ListQualifyingPoints(conceptID, requireVerified)
	if err != nil {
		slog.Warn("wiki: claim verify list qualifying points failed, skipping", "page_id", pageID, "error", err)
		return
	}
	pointContent := make(map[string]string, len(qualifying))
	for _, p := range qualifying {
		pointContent[p.PointID] = p.Content
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

	qualifying, err := s.store.ListQualifyingPoints(conceptIDOf(page), !isManualCompiledFrom(page.CompiledFrom))
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
	if err := s.indexPage(page); err != nil {
		slog.Error("wiki: index page after publish failed", "page_id", pageID, "error", err)
	}

	if page.PageType != PageTypeTopic {
		if err := s.RecomputeRelationsForPage(pageID); err != nil {
			slog.Error("wiki: recompute relations after publish failed", "page_id", pageID, "error", err)
		}
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
	s.cascadeToParentTopics(pageID, "archived")

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
	s.cascadeToParentTopics(pageID, reason)
	return nil
}

// cascadeToParentTopics implements docs/impl/v1/wiki.md 步骤 9: a concept or
// fact page entering needs_recompile or archived propagates to its
// containing topic page(s) (needs_recompile only — topic pages don't
// propagate further, "只有两层"). No-op for topic pages themselves (contains
// is only ever concept/fact -> topic in the ContainingTopics lookup
// direction, so this is naturally a no-op when called with a topic page id,
// but the page_type check makes the intent explicit and avoids an extra
// store round-trip).
func (s *Service) cascadeToParentTopics(memberPageID, memberReason string) {
	page, err := s.store.GetPage(memberPageID)
	if err != nil || page == nil || page.PageType == PageTypeTopic {
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

// GetActivePageByEntryID exposes the store lookup for callers outside this
// package (docs/impl/v1/concept-evolution.md 步骤 3 merge 执行: find the page
// tied to a concept being merged so it can be flagged needs_recompile).
func (s *Service) GetActivePageByEntryID(conceptID string) (*Page, error) {
	return s.store.GetActivePageByEntryID(conceptID)
}

// RecompileFlag is one page ScanForNewQualifyingKP marked needs_recompile,
// returned so the caller (Study) can write the recompile_flag audit trail
// (docs/impl/v1/study.md 步骤 6, docs/impl/v1/wiki.md 步骤 5b).
type RecompileFlag struct {
	PageID  string
	EntryID string
	Reason  string
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
		if !p.EntryID.Valid {
			continue
		}
		current, ok := currentQualifyingCounts[p.EntryID.String]
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
		flagged = append(flagged, RecompileFlag{PageID: p.PageID, EntryID: p.EntryID.String, Reason: reason})
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

// NotifyLinkVerified implements activation.WikiNotifier (docs/impl/v1/wiki.md
// 步骤5 触发(d)): any published page whose source_point_ids contains pointID
// is marked needs_recompile, so a stale wiki_pages.observed_conditions
// (synced from VerifiedLinksObservedConditions only at compile time) picks up
// the KP's just-verified confirming questions on the next recompile.
func (s *Service) NotifyLinkVerified(pointID string) error {
	if pointID == "" {
		return nil
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
			if pid == pointID {
				affected = true
				break
			}
		}
		if !affected {
			continue
		}
		if err := s.MarkNeedsRecompile(p.PageID, "link_verified"); err != nil {
			slog.Error("wiki: mark needs_recompile from link verified failed", "page_id", p.PageID, "error", err)
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

	conceptHits, err := s.matchEntryRow(question)
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
	seenEntry := make(map[string]bool)
	var skeleton *SkeletonInfo
	for _, pageID := range rawHits {
		page, err := s.store.GetPage(pageID)
		if err != nil || page == nil {
			continue
		}
		if page.PageType != PageTypeTopic {
			if !seenEntry[pageID] {
				seenEntry[pageID] = true
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
			if !seenEntry[m] {
				seenEntry[m] = true
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

	querySubject, qi, qa, qc := activation.BuildQueryConditionTerms(subject, intent, audience, constraint)

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
			if !activation.MatchConditionGroups([]activation.ObservedCondition{cond}, querySubject, qi, qa, qc) {
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
