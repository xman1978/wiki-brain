package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/evidence"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/session"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

type Service struct {
	store         *Store
	llmClient     llm.LLMClient
	unitsIndex    bleve.Index
	pointsIndex   bleve.Index
	outlinesIndex bleve.Index
	cfg           *config.Config
	activationSvc *activation.Service
	evidenceSvc   *evidence.Service
	wikiSvc       *wiki.Service
}

const (
	defaultRerankJudgeBatchMaxChars = 4000
	defaultRerankJudgeConcurrency   = 4
)

func NewService(store *Store, llmClient llm.LLMClient, unitsIdx, pointsIdx, outlinesIdx bleve.Index, cfg *config.Config, activationSvc *activation.Service, evidenceSvc *evidence.Service, wikiSvc *wiki.Service) *Service {
	return &Service{
		store:         store,
		llmClient:     llmClient,
		unitsIndex:    unitsIdx,
		pointsIndex:   pointsIdx,
		outlinesIndex: outlinesIdx,
		cfg:           cfg,
		activationSvc: activationSvc,
		evidenceSvc:   evidenceSvc,
		wikiSvc:       wikiSvc,
	}
}

// FastPathFallbackEnabled reports whether Answer should redo a failed
// fast-path answer via the slow path (docs/impl/v1/retrieval.md 步骤 6b);
// centralized here since Retrieval owns the retrieval.* config section.
func (s *Service) FastPathFallbackEnabled() bool {
	return s.cfg.Retrieval.FastPathFallback
}

func (s *Service) Retrieve(ctx context.Context, question string) (*EvidenceSet, error) {
	return s.RetrieveWithProgress(ctx, QueryContext{Question: question}, nil)
}

// RetrieveWithProgress dispatches to the activation fast path
// (docs/impl/v1/retrieval.md 步骤 2) unless ForceFull is set or fast_path is
// disabled, falling back to the full MVP pipeline whenever the fast path
// can't be built or Match finds nothing. When fast_path=false the match is
// still performed and its activation_hits are merged into the slow-path
// result — "记录命中日志后仍走慢路径" — so gated rollout can compare hit
// quality without changing behavior.
func (s *Service) RetrieveWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	if !qc.ForceFull {
		wikiES, skeletonPageID, skeletonMembers, ok := s.tryWikiAnswer(ctx, qc)
		if ok {
			return wikiES, nil
		}
		fastES, hits, ok := s.tryFastPath(ctx, qc)
		if ok {
			fastES.SkeletonPageID = skeletonPageID
			fastES.SkeletonMembers = skeletonMembers
			return fastES, nil
		}
		if len(hits) > 0 {
			slowES, err := s.retrieveSlowPathWithSkeleton(ctx, qc, progress, skeletonPageID, skeletonMembers)
			if err != nil {
				return nil, err
			}
			slowES.ActivationHits = hits
			return slowES, nil
		}
		return s.retrieveSlowPathWithSkeleton(ctx, qc, progress, skeletonPageID, skeletonMembers)
	}
	return s.retrieveSlowPath(ctx, qc, progress)
}

// tryWikiAnswer implements docs/impl/v1/retrieval.md 第 0 层: query the Wiki
// index before the activation fast path (不调 LLM 除非命中分达标), and if the
// hit page can sufficiently answer the question, return a path_type=wiki
// EvidenceSet without ever reaching the fast/slow path. wikiSvc is nil until
// main.go wires it up, matching the doc's "未实现时第 0 层跳过". The second and
// third return values are the topic-page skeleton (docs/impl/v1/wiki.md 步骤
// 8「检索接入」) — set whenever a topic page was hit and expanded, regardless
// of whether direct answer itself succeeded, so callers can carry it forward
// into the fast/slow path either way.
func (s *Service) tryWikiAnswer(ctx context.Context, qc QueryContext) (*EvidenceSet, string, []SkeletonMemberInfo, bool) {
	if s.wikiSvc == nil {
		return nil, "", nil, false
	}
	result, ok, skeleton, err := s.wikiSvc.TryDirectAnswer(ctx, qc.Question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, s.cfg.Retrieval.WikiMinScore, s.cfg.Retrieval.WikiMaxCandidates)
	if err != nil {
		slog.Warn("wiki direct-answer failed, falling back", "error", err)
		return nil, "", nil, false
	}

	var skeletonPageID string
	var skeletonMembers []SkeletonMemberInfo
	if skeleton != nil {
		skeletonPageID = skeleton.PageID
		for _, m := range skeleton.Members {
			skeletonMembers = append(skeletonMembers, SkeletonMemberInfo{PageID: m.PageID, PointIDs: m.PointIDs})
		}
	}

	if !ok {
		return nil, skeletonPageID, skeletonMembers, false
	}
	return &EvidenceSet{
		Question:          qc.Question,
		Subject:           qc.Subject,
		Intent:            qc.Intent,
		Audience:          qc.Audience,
		Constraint:        qc.Constraint,
		Path:              "direct",
		PathType:          PathTypeWiki,
		ActivationHits:    []ActivationHit{},
		DirectEvidence:    []Evidence{},
		Supporting:        []Evidence{},
		WikiPageID:        result.PageID,
		CitedPointIDs:     result.CitedPointIDs,
		WikiAnswerContent: result.Content,
		SkeletonPageID:    skeletonPageID,
		SkeletonMembers:   skeletonMembers,
	}, skeletonPageID, skeletonMembers, true
}

// RetrieveSlowPathWithProgress forces the full MVP pipeline, used by Answer's
// step-6b fallback to redo retrieval after a fast-path answer fails
// (docs/impl/v1/retrieval.md 步骤 6).
func (s *Service) RetrieveSlowPathWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	return s.retrieveSlowPath(ctx, qc, progress)
}

// retrieveSlowPathWithSkeleton implements docs/impl/v1/wiki.md 步骤 8「检索接
// 入」两层架构扩展 for the slow path: when a topic page was hit (regardless of
// whether Wiki direct-answer succeeded), tag the resulting EvidenceSet with
// skeleton_page_id/members for observability (question_complexity's
// skeleton_used_count, docs/impl/v1/study.md 步骤 7) even when injection
// itself is gated off. Actual candidate injection only happens when
// retrieval.skeleton_injection_enabled is true (决策 B, 默认关闭).
func (s *Service) retrieveSlowPathWithSkeleton(ctx context.Context, qc QueryContext, progress ProgressFunc, skeletonPageID string, skeletonMembers []SkeletonMemberInfo) (*EvidenceSet, error) {
	if skeletonPageID == "" || !s.cfg.Retrieval.SkeletonInjectionEnabled {
		es, err := s.retrieveSlowPath(ctx, qc, progress)
		if err != nil {
			return nil, err
		}
		if skeletonPageID != "" {
			es.SkeletonPageID = skeletonPageID
			es.SkeletonMembers = skeletonMembers
		}
		return es, nil
	}

	pointIDs := uniqueSkeletonPointIDs(skeletonMembers)
	skeletonCandidates, err := s.buildSkeletonCandidates(pointIDs)
	if err != nil {
		slog.Warn("retrieval: build skeleton candidates failed, continuing without injection", "error", err)
	}

	rerankTopN := s.cfg.Retrieval.RerankTopN
	var es *EvidenceSet
	if rerankTopN > 0 && len(skeletonCandidates) >= rerankTopN {
		// Decision A, full-bypass branch: injection alone already meets
		// rerank_top_n — skip Domain/Source prefilter and Outline/FTS/RRF
		// entirely (3 LLM calls total: mining + rerank + answer).
		emit := func(phase, status, detail string, dur int64) {
			if progress != nil {
				progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
			}
		}
		es, err = s.rerankAndBuildEvidenceSet(ctx, qc, skeletonCandidates, emit, progress, false)
	} else {
		// Decision A, supplement branch: prefilter/outline/FTS still run;
		// skeleton candidates are merged in before Rerank (recallFromSources).
		es, err = s.retrieveSlowPathInternal(ctx, qc, progress, skeletonCandidates)
	}
	if err != nil {
		return nil, err
	}
	es.SkeletonPageID = skeletonPageID
	es.SkeletonMembers = skeletonMembers
	return es, nil
}

func uniqueSkeletonPointIDs(members []SkeletonMemberInfo) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range members {
		for _, pid := range m.PointIDs {
			if !seen[pid] {
				seen[pid] = true
				out = append(out, pid)
			}
		}
	}
	return out
}

// buildSkeletonCandidates reverse-looks-up each skeleton point_id's KU
// (lifecycle=current on both, same as the fast path — docs/impl/v1/wiki.md
// 步骤 8), producing pre-ranked candidates tagged sourcePaths=["skeleton"] so
// they're indistinguishable from any other candidate once inside Rerank.
func (s *Service) buildSkeletonCandidates(pointIDs []string) ([]candidate, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: get current units by skeleton points: %w", err)
	}
	out := make([]candidate, 0, len(hits))
	for _, h := range hits {
		out = append(out, candidate{
			candidateID: h.UnitID,
			unitID:      h.UnitID,
			pointID:     h.PointID,
			sourceID:    h.SourceID,
			lineStart:   h.LineStart,
			lineEnd:     h.LineEnd,
			score:       1.0,
			sourcePaths: []string{"skeleton"},
		})
	}
	return out, nil
}

// mergeSkeletonCandidates appends skeleton candidates not already present
// (by unitID) into merged, tagging sourcePaths with "skeleton" on top of
// whatever recall path(s) already found it — a unit that both a normal recall
// path and the skeleton agree on keeps its original entry unmodified.
func mergeSkeletonCandidates(merged, skeleton []candidate) []candidate {
	if len(skeleton) == 0 {
		return merged
	}
	seen := make(map[string]bool, len(merged))
	for _, c := range merged {
		seen[c.unitID] = true
	}
	for _, c := range skeleton {
		if seen[c.unitID] {
			continue
		}
		seen[c.unitID] = true
		merged = append(merged, c)
	}
	return merged
}

// tryFastPath implements docs/impl/v1/retrieval.md 步骤 2. It returns
// (evidenceSet, activationHits, true) on a usable fast-path result; when the
// fast path isn't viable it returns (nil, activationHits, false) — the
// caller falls back to the slow path but keeps activationHits so they still
// end up in the (path_type=full) EvidenceSet for Trace to grade as failures.
func (s *Service) tryFastPath(ctx context.Context, qc QueryContext) (*EvidenceSet, []ActivationHit, bool) {
	if s.activationSvc == nil {
		return nil, nil, false
	}

	matchCfg := activation.MatchConfig{
		MatchTop: s.cfg.Retrieval.ActivationMatchTop,
	}
	expandedQuery := session.ExpandedQuery{
		ExpandedQuestion: qc.Question,
		Subject:          qc.Subject,
		Intent:           qc.Intent,
		Audience:         qc.Audience,
		Constraint:       qc.Constraint,
	}
	matches, err := s.activationSvc.Match(expandedQuery, matchCfg)
	if err != nil {
		slog.Warn("retrieval: activation match failed, falling back to slow path", "error", err)
		return nil, nil, false
	}
	if len(matches) == 0 {
		return nil, nil, false
	}

	activationHits := make([]ActivationHit, len(matches))
	allLinkIDs := make([]string, len(matches))
	for i, m := range matches {
		activationHits[i] = ActivationHit{LinkID: m.Link.LinkID, PointID: m.Link.PointID, MatchScore: m.Score}
		allLinkIDs[i] = m.Link.LinkID
	}
	slog.Info("retrieval: activation layer matched", "link_count", len(matches), "link_ids", allLinkIDs)

	// Candidates match for signal recording (activation_success/failure) but
	// never answer on the fast path — only verified links may.
	var verified []activation.LinkMatch
	for _, m := range matches {
		if m.Link.Status == activation.StatusVerified {
			verified = append(verified, m)
		}
	}
	if len(verified) == 0 {
		slog.Info("retrieval: activation matched candidate-only, recording hits and falling back to slow path",
			"link_ids", allLinkIDs)
		return nil, activationHits, false
	}

	linkIDs := make([]string, len(verified))
	pointIDs := make([]string, len(verified))
	for i, m := range verified {
		linkIDs[i] = m.Link.LinkID
		pointIDs[i] = m.Link.PointID
	}

	// A precise-hit cache only means something when there's exactly one
	// verified link — every activation score is 1.0 (exact match, no
	// similarity ranking), so >1 distinct verified link matching the same
	// query is ambiguity, not precision. Fall back to the slow path, still
	// returning every ActivationHit (including candidates) for Trace.
	if len(verified) > 1 {
		slog.Info("retrieval: activation matched more than one verified link, ambiguous, falling back to slow path",
			"link_ids", linkIDs)
		return nil, activationHits, false
	}

	// Async, non-blocking — only touch the verified link that may answer.
	go func() {
		if err := s.activationSvc.TouchLastUsed(linkIDs); err != nil {
			slog.Warn("retrieval: touch last used failed", "error", err)
		}
	}()

	if !s.cfg.Retrieval.FastPath {
		return nil, activationHits, false
	}

	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: fast path unit lookup failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}
	if len(hits) == 0 {
		// Match already filters to verified+current, so this shouldn't happen;
		// treat as a fallback rather than an error.
		slog.Warn("retrieval: fast path found no current KU for matched links, falling back", "link_ids", linkIDs)
		return nil, activationHits, false
	}

	seen := make(map[string]bool, len(hits))
	var direct []candidate
	for _, h := range hits {
		if seen[h.UnitID] {
			continue
		}
		seen[h.UnitID] = true
		direct = append(direct, candidate{
			unitID:      h.UnitID,
			pointID:     h.PointID,
			sourceID:    h.SourceID,
			lineStart:   h.LineStart,
			lineEnd:     h.LineEnd,
			sourcePaths: []string{"direct"},
		})
	}

	allCandidates, conflictCandidates, err := s.kpnExpand(direct)
	if err != nil {
		slog.Warn("retrieval: fast path kpn expand failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}

	var directCands, supportingCands []candidate
	for _, c := range allCandidates {
		switch c.sourcePaths[0] {
		case "direct":
			directCands = append(directCands, c)
		case "supporting":
			supportingCands = append(supportingCands, c)
		}
	}

	es, err := s.buildEvidenceSet(ctx, qc.Question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, "short", directCands, supportingCands, conflictCandidates, nil, false)
	if err != nil {
		slog.Warn("retrieval: fast path build evidence set failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}

	// "direct 候选挖掘后片段全部为空且整段回退也为空（KU 正文读取失败等异常）
	// → 视为快路径失败": Evidence Mining always keeps at least a whole-segment
	// item for a direct candidate (mined=false) unless its KU content couldn't
	// even be read, so an empty DirectEvidence here means that read failed.
	if len(es.DirectEvidence) == 0 {
		slog.Warn("retrieval: fast path produced no direct evidence, falling back to slow path", "link_ids", linkIDs)
		return nil, activationHits, false
	}

	if s.cfg.Retrieval.FastPathVerify {
		sufficient, err := s.verifyFastPathEvidence(ctx, qc.Question, es)
		if err != nil {
			slog.Warn("retrieval: fast path verify failed, falling back to slow path", "error", err)
			return nil, activationHits, false
		}
		if !sufficient {
			slog.Info("retrieval: fast path verify judged evidence insufficient, falling back to slow path", "link_ids", linkIDs)
			return nil, activationHits, false
		}
	}

	es.PathType = PathTypeFast
	es.ActivationHits = activationHits
	slog.Info("retrieval: fast path evidence built",
		"direct", len(es.DirectEvidence), "supporting", len(es.Supporting), "link_ids", linkIDs)
	return es, activationHits, true
}

// verifyFastPathEvidence implements docs/impl/v1/retrieval.md 步骤 2a: a
// single LLM call judging whether the fast path's evidence (built from an
// exact quadruple match, without Rerank) still independently and completely
// answers the question. A match on the activation condition doesn't
// guarantee the KP's content is still adequate — it may have been updated,
// or the question may carry detail the quadruple didn't capture — so this
// is the only check standing between an exact-match hit and answering the
// user directly. Any ambiguity (LLM error, unparseable response,
// sufficient=false) is treated as failure; the caller falls back to the
// slow path.
func (s *Service) verifyFastPathEvidence(ctx context.Context, question string, es *EvidenceSet) (bool, error) {
	var evidenceText strings.Builder
	for i, ev := range es.DirectEvidence {
		fmt.Fprintf(&evidenceText, "[direct-%d] %s\n", i+1, ev.Content)
	}
	for i, ev := range es.Supporting {
		fmt.Fprintf(&evidenceText, "[supporting-%d] %s\n", i+1, ev.Content)
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "fast_verify.md", map[string]string{
		"question": question,
		"evidence": evidenceText.String(),
	}, "classification")
	if err != nil {
		return false, fmt.Errorf("retrieval: fast path verify call: %w", err)
	}

	var result struct {
		Sufficient bool   `json:"sufficient"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return false, fmt.Errorf("retrieval: parse fast path verify response: %w", err)
	}
	if !result.Sufficient {
		slog.Info("retrieval: fast path verify judged insufficient", "reason", result.Reason)
	}
	return result.Sufficient, nil
}

func (s *Service) retrieveSlowPath(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	return s.retrieveSlowPathInternal(ctx, qc, progress, nil)
}

func (s *Service) retrieveSlowPathInternal(ctx context.Context, qc QueryContext, progress ProgressFunc, skeleton []candidate) (*EvidenceSet, error) {
	emit := func(phase, status, detail string, dur int64) {
		if progress != nil {
			progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
		}
	}

	// Step 2: Domain pre-filter
	domainStart := time.Now()
	candidateSources, err := s.domainPreFilter(ctx, qc.Question)
	if err != nil {
		emit("activation", "error", err.Error(), time.Since(domainStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: domain pre-filter: %w", err)
	}
	slog.Info("retrieval: step2 domain pre-filter done", "candidates", len(candidateSources))

	es, err := s.filterAndRecall(ctx, qc, candidateSources, emit, progress, false, skeleton)
	if err != nil {
		return nil, err
	}
	if !evidenceEmpty(es) {
		return es, nil
	}

	// Fallback 1（问答准确率测试 2026-07-16 诊断，方向 C）：结果为空且问题没带
	// subject/intent（如直连 POST /answer，未经 /session/turn 解析）时，补做一次
	// 单轮解析再整链路重试一遍——Source 语义过滤和 rerank judge 都会用 subject/intent
	// 做语义判断，裸问题被字面关键词带偏导致选错文档/误判不相关的情况，补上后可能自愈。
	retryQC := qc
	if qc.Subject == "" && qc.Intent == "" {
		parsed := session.NewParser(s.llmClient).Parse(ctx, qc.Question, &session.SessionState{})
		if parsed.Subject != "" || parsed.Intent != "" {
			retryQC.Subject = parsed.Subject
			retryQC.Intent = parsed.Intent
			slog.Info("retrieval: empty result, retrying with extracted subject/intent",
				"subject", parsed.Subject, "intent", parsed.Intent)
			es, err = s.filterAndRecall(ctx, retryQC, candidateSources, emit, progress, false, skeleton)
			if err != nil {
				return nil, err
			}
			if !evidenceEmpty(es) {
				return es, nil
			}
		}
	}

	// Fallback 2（方向 A）：仍为空，跳过 Source 语义过滤，直接在 Domain 预过滤后的全部
	// 候选 source 上召回——单段摘要天然覆盖不了多主题文档（如"问题汇总"类）的所有子话题，
	// Source 过滤会系统性漏选正确文档，只能放宽候选池交给 outline/FTS 召回自己发现。
	// This is also the last attempt before giving up entirely, so it passes
	// lastResort=true through to evidence mining（问答准确率测试 2026-07-17
	// 诊断）：rerank_judge 只看预抽取的语义/KP 摘要，摘要遗漏问题关键事实时
	// 唯一候选会被判成 supporting 而非 direct；供 direct 用的整段回退规则本身没变
	// （docs/impl/v1/evidence.md），只是在最后一次重试里把这条回退也对 supporting
	// 开放，避免"rerank 已经判定候选主题相关、只是摘要没抓到关键句"这种情况直接判空。
	slog.Info("retrieval: empty result, retrying against full domain-filtered source pool", "sources", len(candidateSources))
	return s.recallFromSources(ctx, retryQC, candidateSources, emit, progress, true, skeleton)
}

func evidenceEmpty(es *EvidenceSet) bool {
	return es == nil || (len(es.DirectEvidence) == 0 && len(es.Supporting) == 0)
}

// filterAndRecall runs Step 3 (Source semantic filter) then Steps 4-10 within
// the filtered source set.
func (s *Service) filterAndRecall(ctx context.Context, qc QueryContext, candidateSources []SourceInfo, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool, skeleton []candidate) (*EvidenceSet, error) {
	activationStart := time.Now()
	emit("activation", "start", "", 0)

	filteredSources, err := s.sourceSemanticFilter(ctx, qc, candidateSources)
	if err != nil {
		emit("activation", "error", err.Error(), time.Since(activationStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: source filter: %w", err)
	}

	sourceIDs := make([]string, len(filteredSources))
	for i, src := range filteredSources {
		sourceIDs[i] = src.SourceID
	}
	slog.Info("retrieval: step3 source filter done", "sources", sourceIDs)
	emit("activation", "done", fmt.Sprintf("%d 个来源", len(sourceIDs)), time.Since(activationStart).Milliseconds())

	return s.recallFromSources(ctx, qc, filteredSources, emit, progress, lastResort, skeleton)
}

// recallFromSources runs Steps 4-10 (outline+FTS recall through
// EvidenceSet construction) against a fixed source set. lastResort is
// forwarded to buildEvidenceSet — see retrieveSlowPath's fallback 2 for what
// it changes. skeleton (docs/impl/v1/wiki.md 步骤 8「检索接入」) is merged into
// the RRF-merged candidate set before Rerank when non-empty — this is
// decision A's "不足时保留预过滤走混合召回" branch (prefilter/outline/FTS still
// run; skeleton only supplements them).
func (s *Service) recallFromSources(ctx context.Context, qc QueryContext, sources []SourceInfo, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool, skeleton []candidate) (*EvidenceSet, error) {
	question := qc.Question
	sourceIDs := make([]string, len(sources))
	for i, src := range sources {
		sourceIDs[i] = src.SourceID
	}

	// Step 4: 目录结构检索（Outline recall）
	emit("outline", "start", "", 0)
	outlineStart := time.Now()
	outlineCandidates, err := s.outlineRecall(ctx, qc, sourceIDs)
	if err != nil {
		emit("outline", "error", err.Error(), time.Since(outlineStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: outline recall: %w", err)
	}
	slog.Info("retrieval: step4 outline recall done", "candidates", len(outlineCandidates))
	emit("outline", "done", fmt.Sprintf("%d 条", len(outlineCandidates)), time.Since(outlineStart).Milliseconds())

	// Step 5: 全文检索（FTS recall）— question 一路 + 四元组一路，各自成榜后进 RRF
	emit("fts", "start", "", 0)
	ftsStart := time.Now()
	ftsQuestion, err := s.ftsRecall(question, sourceIDs, "fts")
	if err != nil {
		emit("fts", "error", err.Error(), time.Since(ftsStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: fts recall: %w", err)
	}
	tupleText := queryTupleText(qc)
	var ftsTuple []candidate
	if tupleText != "" && tupleText != question {
		ftsTuple, err = s.ftsRecall(tupleText, sourceIDs, "fts_tuple")
		if err != nil {
			emit("fts", "error", err.Error(), time.Since(ftsStart).Milliseconds())
			return nil, fmt.Errorf("retrieval: fts tuple recall: %w", err)
		}
	}
	slog.Info("retrieval: step5 fts recall done",
		"fts", len(ftsQuestion), "fts_tuple", len(ftsTuple), "tuple_query", tupleText)
	emit("fts", "done", fmt.Sprintf("%d+%d 条", len(ftsQuestion), len(ftsTuple)), time.Since(ftsStart).Milliseconds())

	// Step 6: RRF merge（outline + fts(question) + fts(四元组)）
	merged := s.rrfMerge(outlineCandidates, ftsQuestion, ftsTuple)
	merged = mergeSkeletonCandidates(merged, skeleton)
	slog.Info("retrieval: step6 rrf merge done", "merged", len(merged))

	if len(merged) == 0 {
		emit("rerank", "done", "无候选", 0)
		return &EvidenceSet{
			Question:       question,
			Subject:        qc.Subject,
			Intent:         qc.Intent,
			Audience:       qc.Audience,
			Constraint:     qc.Constraint,
			Path:           "deep",
			PathType:       PathTypeFull,
			ActivationHits: []ActivationHit{},
			DirectEvidence: []Evidence{},
			Supporting:     []Evidence{},
			Conflicts:      nil,
			GapReason:      GapReasonNoCandidates,
		}, nil
	}

	return s.rerankAndBuildEvidenceSet(ctx, qc, merged, emit, progress, lastResort)
}

// rerankAndBuildEvidenceSet runs Steps 7-10 (Rerank through EvidenceSet
// construction) given an already-assembled candidate set. Factored out of
// recallFromSources so the skeleton-injection full-bypass branch
// (docs/impl/v1/wiki.md 步骤 8「检索接入」, decision A: skeleton_point_ids ≥
// rerank_top_n skips Domain/Source prefilter and Outline/FTS/RRF entirely)
// can reuse it without going through Domain/Source/Outline/FTS at all.
func (s *Service) rerankAndBuildEvidenceSet(ctx context.Context, qc QueryContext, merged []candidate, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool) (*EvidenceSet, error) {
	question := qc.Question

	// Step 7: 证据分类（LLM Rerank）
	emit("rerank", "start", fmt.Sprintf("%d 条候选", len(merged)), 0)
	rerankStart := time.Now()
	reranked, filteredEvidence, err := s.rerank(ctx, qc, merged)
	if err != nil {
		emit("rerank", "error", err.Error(), time.Since(rerankStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: rerank: %w", err)
	}
	slog.Info("retrieval: step7 rerank done", "kept", len(reranked), "filtered", len(filteredEvidence))

	// Step 8: KPN expansion
	reranked, conflictCandidates, err := s.kpnExpand(reranked)
	if err != nil {
		return nil, fmt.Errorf("retrieval: kpn expand: %w", err)
	}

	// Step 9: Sufficiency check
	path := "deep"
	var direct, supporting []candidate
	for _, c := range reranked {
		switch c.sourcePaths[0] {
		case "direct":
			direct = append(direct, c)
		case "supporting":
			supporting = append(supporting, c)
		}
	}
	if len(direct) > 0 {
		path = "short"
	}
	slog.Info("retrieval: step9 sufficiency", "path", path, "direct", len(direct), "supporting", len(supporting), "conflicts", len(conflictCandidates))
	emit("rerank", "done", fmt.Sprintf("%d 直接 · %d 补充", len(direct), len(supporting)), time.Since(rerankStart).Milliseconds())

	// Step 10: Build EvidenceSet
	es, err := s.buildEvidenceSet(ctx, question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, path, direct, supporting, conflictCandidates, progress, lastResort)
	if err != nil {
		return nil, fmt.Errorf("retrieval: build evidence set: %w", err)
	}
	es.FilteredEvidence = filteredEvidence
	if len(direct) == 0 && len(supporting) == 0 {
		es.GapReason = GapReasonJudgeFiltered
	}
	return es, nil
}

// Step 2: Domain pre-filter
func (s *Service) domainPreFilter(ctx context.Context, question string) ([]SourceInfo, error) {
	domains, err := s.store.ListDomains()
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return s.store.ListAllSources()
	}

	var domainList strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&domainList, "[%s] %s：%s\n", d.DomainID, d.Name, d.Description)
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "question_domain_match.md", map[string]string{
		"question":    question,
		"domain_list": domainList.String(),
	}, "classification")
	if err != nil {
		slog.Warn("retrieval: domain match failed, skipping", "error", err)
		return s.store.ListAllSources()
	}

	var result struct {
		DomainIDs []string `json:"domain_ids"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("retrieval: domain match parse failed, skipping", "error", err)
		return s.store.ListAllSources()
	}

	if len(result.DomainIDs) == 0 {
		return s.store.ListAllSources()
	}

	sources, err := s.store.ListSourcesByDomainIDs(result.DomainIDs)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		slog.Warn("retrieval: domain match matched zero sources, falling back to all sources", "domain_ids", result.DomainIDs)
		return s.store.ListAllSources()
	}
	return sources, nil
}

// Step 3: Source semantic filter
func (s *Service) sourceSemanticFilter(ctx context.Context, qc QueryContext, candidates []SourceInfo) ([]SourceInfo, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	var sourceList strings.Builder
	for _, src := range candidates {
		summary := ""
		if src.Summary.Valid {
			summary = src.Summary.String
		}
		if summary != "" {
			fmt.Fprintf(&sourceList, "[%s] 标题：%s / 概述：%s\n", src.SourceID, src.Title, summary)
		} else {
			fmt.Fprintf(&sourceList, "[%s] 标题：%s\n", src.SourceID, src.Title)
		}
	}

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}
	audience := qc.Audience
	if audience == "" {
		audience = "（未提取）"
	}
	constraint := qc.Constraint
	if constraint == "" {
		constraint = "（无）"
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "source_filter.md", map[string]string{
		"question":    qc.Question,
		"subject":     subject,
		"intent":      intent,
		"audience":    audience,
		"constraint":  constraint,
		"source_list": sourceList.String(),
	}, "classification")
	if err != nil {
		slog.Warn("retrieval: source filter failed, using all candidates", "error", err)
		return candidates, nil
	}

	var result struct {
		SourceIDs []string `json:"source_ids"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("retrieval: source filter parse failed, using all candidates", "error", err)
		return candidates, nil
	}

	if len(result.SourceIDs) == 0 {
		return candidates, nil
	}

	idSet := make(map[string]bool, len(result.SourceIDs))
	for _, id := range result.SourceIDs {
		idSet[id] = true
	}
	var filtered []SourceInfo
	for _, src := range candidates {
		if idSet[src.SourceID] {
			filtered = append(filtered, src)
		}
	}
	if len(filtered) == 0 {
		return candidates, nil
	}
	return filtered, nil
}

// Step 4: Outline recall
func (s *Service) outlineRecall(ctx context.Context, qc QueryContext, sourceIDs []string) ([]candidate, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}

	// 目录召回按核心主题+意图整体匹配，而非裸问题文本——问题需经 session parser
	// 解析才完整（如指代消解），裸问题字面匹配容易因同义/近义关键词误召回
	// 场景不同的章节（如"实施"既出现在实施考核场景，也出现在销售代理场景）。
	matchText := strings.TrimSpace(qc.Subject + " " + qc.Intent)
	if matchText == "" {
		matchText = qc.Question
	}

	threshold := s.cfg.Retrieval.OutlineFTSMinScore
	if threshold <= 0 {
		threshold = 0.5
	}

	// 4.1 FTS on outlines index
	type sourceScore struct {
		maxScore   float64
		outlineIDs []string
	}
	scoreBySource := make(map[string]*sourceScore)
	for _, sid := range sourceIDs {
		scoreBySource[sid] = &sourceScore{}
	}

	q := bleve.NewMatchQuery(matchText)
	searchReq := bleve.NewSearchRequest(q)
	searchReq.Size = 100
	searchReq.Fields = []string{"source_id", "outline_id"}

	results, err := s.outlinesIndex.Search(searchReq)
	if err != nil {
		slog.Warn("retrieval: outline fts failed", "error", err)
	} else {
		for _, hit := range results.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			ss, ok := scoreBySource[sid]
			if !ok {
				continue
			}
			if hit.Score > ss.maxScore {
				ss.maxScore = hit.Score
			}
			ss.outlineIDs = append(ss.outlineIDs, hit.ID)
		}
	}

	// Collect FTS hits from sources above threshold
	var ftsOutlineIDs []string
	var lowScoreSources []string
	for sid, ss := range scoreBySource {
		if ss.maxScore >= threshold {
			ftsOutlineIDs = append(ftsOutlineIDs, ss.outlineIDs...)
		} else {
			lowScoreSources = append(lowScoreSources, sid)
		}
	}

	// 4.2 LLM fallback for low-score sources
	if len(lowScoreSources) > 0 {
		llmOutlineIDs, err := s.outlineLLMFallback(ctx, qc, lowScoreSources)
		if err != nil {
			slog.Warn("retrieval: outline llm fallback error", "error", err)
		} else {
			ftsOutlineIDs = append(ftsOutlineIDs, llmOutlineIDs...)
		}
	}

	if len(ftsOutlineIDs) == 0 {
		return nil, nil
	}

	// Expand to children and get units
	var allOutlineIDs []string
	sourceIDSet := make(map[string]bool)
	for _, sid := range sourceIDs {
		sourceIDSet[sid] = true
	}

	// Group outline IDs by source for child expansion
	outlinesBySource := make(map[string][]string)
	outlines, err := s.store.GetOutlinesBySourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}
	outlineSourceMap := make(map[string]string)
	for _, o := range outlines {
		outlineSourceMap[o.OutlineID] = o.SourceID
	}
	for _, oid := range ftsOutlineIDs {
		if sid, ok := outlineSourceMap[oid]; ok {
			outlinesBySource[sid] = append(outlinesBySource[sid], oid)
		}
	}

	for sid, oids := range outlinesBySource {
		children, err := s.store.GetChildOutlineIDs(oids, sid)
		if err != nil {
			return nil, err
		}
		allOutlineIDs = append(allOutlineIDs, children...)
	}

	units, err := s.store.GetUnitsByOutlineIDs(allOutlineIDs)
	if err != nil {
		return nil, err
	}

	var candidates []candidate
	for _, u := range units {
		pointID, err := s.store.GetFirstPointByUnitID(u.UnitID)
		if err != nil {
			slog.Warn("retrieval: outline recall unit has no KP, skipping", "unit_id", u.UnitID)
			continue
		}
		candidates = append(candidates, candidate{
			unitID:      u.UnitID,
			pointID:     pointID,
			sourceID:    u.SourceID,
			lineStart:   u.LineStart,
			lineEnd:     u.LineEnd,
			score:       1.0,
			sourcePaths: []string{"outline"},
		})
	}
	return candidates, nil
}

func (s *Service) outlineLLMFallback(ctx context.Context, qc QueryContext, sourceIDs []string) ([]string, error) {
	outlines, err := s.store.GetOutlinesBySourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}

	// Group outlines by source
	bySource := make(map[string][]OutlineInfo)
	for _, o := range outlines {
		bySource[o.SourceID] = append(bySource[o.SourceID], o)
	}

	type result struct {
		outlineIDs []string
		err        error
	}

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}

	var mu sync.Mutex
	var allIDs []string
	var wg sync.WaitGroup

	for sid, sourceOutlines := range bySource {
		_ = sid
		wg.Add(1)
		go func(ols []OutlineInfo) {
			defer wg.Done()
			var outlineList strings.Builder
			for _, o := range ols {
				indent := strings.Repeat("  ", o.Level-1)
				line := fmt.Sprintf("[%s] %s%s", o.OutlineID, indent, o.Title)
				if o.Summary.Valid && o.Summary.String != "" {
					line += " / 关键词：" + o.Summary.String
				}
				outlineList.WriteString(line + "\n")
			}

			resp, err := s.llmClient.CompleteJSON(ctx, "outline_filter.md", map[string]string{
				"question":     qc.Question,
				"subject":      subject,
				"intent":       intent,
				"outline_list": outlineList.String(),
			}, "classification")
			if err != nil {
				slog.Warn("retrieval: outline llm fallback failed for source", "error", err)
				return
			}

			var parsed struct {
				OutlineIDs []string `json:"outline_ids"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				slog.Warn("retrieval: outline llm parse failed", "error", err)
				return
			}

			mu.Lock()
			allIDs = append(allIDs, parsed.OutlineIDs...)
			mu.Unlock()
		}(sourceOutlines)
	}
	wg.Wait()
	return allIDs, nil
}

// queryTupleText joins non-empty subject/intent/audience/constraint for the
// second FTS path. Empty result means skip fts_tuple (fall back to question-only).
func queryTupleText(qc QueryContext) string {
	parts := make([]string, 0, 4)
	for _, p := range []string{qc.Subject, qc.Intent, qc.Audience, qc.Constraint} {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// Step 5: FTS recall against units + points indexes for one query string.
// path labels the RRF list (e.g. "fts" for the raw question, "fts_tuple" for
// the parsed quadruple).
func (s *Service) ftsRecall(queryText string, sourceIDs []string, path string) ([]candidate, error) {
	if len(sourceIDs) == 0 || strings.TrimSpace(queryText) == "" {
		return nil, nil
	}
	if path == "" {
		path = "fts"
	}

	sourceIDSet := make(map[string]bool, len(sourceIDs))
	for _, id := range sourceIDs {
		sourceIDSet[id] = true
	}

	// unitID → candidate
	unitMap := make(map[string]*candidate)

	// Search units index
	uq := lifecycleCurrentQuery(bleve.NewMatchQuery(queryText))
	uReq := bleve.NewSearchRequest(uq)
	uReq.Size = 100
	uReq.Fields = []string{"unit_id", "source_id", "line_start", "line_end"}

	uResults, err := s.unitsIndex.Search(uReq)
	if err != nil {
		slog.Warn("retrieval: units fts failed", "path", path, "error", err)
	} else {
		for _, hit := range uResults.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			if !sourceIDSet[sid] {
				continue
			}
			uid := hit.ID
			lineStart := intFromField(hit.Fields["line_start"])
			lineEnd := intFromField(hit.Fields["line_end"])

			if existing, ok := unitMap[uid]; ok {
				if hit.Score > existing.score {
					existing.score = hit.Score
				}
			} else {
				unitMap[uid] = &candidate{
					unitID:      uid,
					sourceID:    sid,
					lineStart:   lineStart,
					lineEnd:     lineEnd,
					score:       hit.Score,
					sourcePaths: []string{path},
				}
			}
		}
	}

	// Search points index
	pq := lifecycleCurrentQuery(bleve.NewMatchQuery(queryText))
	pReq := bleve.NewSearchRequest(pq)
	pReq.Size = 100
	pReq.Fields = []string{"point_id", "unit_id", "source_id"}

	// Track best point per unit from points path
	type pointHit struct {
		pointID string
		score   float64
	}
	bestPointPerUnit := make(map[string]pointHit)

	pResults, err := s.pointsIndex.Search(pReq)
	if err != nil {
		slog.Warn("retrieval: points fts failed", "path", path, "error", err)
	} else {
		for _, hit := range pResults.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			if !sourceIDSet[sid] {
				continue
			}
			pointID := hit.ID
			unitID, err := s.store.GetPointUnitID(pointID)
			if err != nil {
				slog.Warn("retrieval: point unit lookup failed", "point_id", pointID, "error", err)
				continue
			}

			if existing, ok := bestPointPerUnit[unitID]; !ok || hit.Score > existing.score {
				bestPointPerUnit[unitID] = pointHit{pointID: pointID, score: hit.Score}
			}

			if existing, ok := unitMap[unitID]; ok {
				if hit.Score > existing.score {
					existing.score = hit.Score
				}
			} else {
				u, err := s.store.GetUnitByID(unitID)
				if err != nil {
					slog.Warn("retrieval: unit lookup failed", "unit_id", unitID, "error", err)
					continue
				}
				unitMap[unitID] = &candidate{
					unitID:      unitID,
					sourceID:    u.SourceID,
					lineStart:   u.LineStart,
					lineEnd:     u.LineEnd,
					score:       hit.Score,
					sourcePaths: []string{path},
				}
			}
		}
	}

	// Assign point_ids
	var result []candidate
	for uid, c := range unitMap {
		if ph, ok := bestPointPerUnit[uid]; ok {
			c.pointID = ph.pointID
		} else {
			pid, err := s.store.GetFirstPointByUnitID(uid)
			if err != nil {
				slog.Warn("retrieval: fts recall unit has no KP, skipping", "unit_id", uid)
				continue
			}
			c.pointID = pid
		}
		result = append(result, *c)
	}
	return result, nil
}

// Step 6: RRF merge across any number of ranked lists (outline, fts, fts_tuple, …).
// Nil/empty lists are skipped. Path label is taken from each list's candidates'
// sourcePaths[0], falling back to "fts".
func (s *Service) rrfMerge(lists ...[]candidate) []candidate {
	const k = 60

	type mergedCandidate struct {
		candidate
		rrfScore float64
		paths    map[string]bool
	}
	merged := make(map[string]*mergedCandidate)

	addRanked := func(candidates []candidate, pathName string) {
		sorted := append([]candidate(nil), candidates...)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].score > sorted[j].score
		})
		for rank, c := range sorted {
			rrfScore := 1.0 / float64(k+rank+1)
			if m, ok := merged[c.unitID]; ok {
				m.rrfScore += rrfScore
				m.paths[pathName] = true
				if c.pointID != "" && m.pointID == "" {
					m.pointID = c.pointID
				}
			} else {
				merged[c.unitID] = &mergedCandidate{
					candidate: c,
					rrfScore:  rrfScore,
					paths:     map[string]bool{pathName: true},
				}
			}
		}
	}

	for _, list := range lists {
		if len(list) == 0 {
			continue
		}
		pathName := "fts"
		if len(list[0].sourcePaths) > 0 && list[0].sourcePaths[0] != "" {
			pathName = list[0].sourcePaths[0]
		}
		addRanked(list, pathName)
	}

	var result []candidate
	for _, m := range merged {
		paths := make([]string, 0, len(m.paths))
		for p := range m.paths {
			paths = append(paths, p)
		}
		m.candidate.score = m.rrfScore
		m.candidate.sourcePaths = paths
		result = append(result, m.candidate)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].score > result[j].score
	})

	topN := s.cfg.Retrieval.RerankTopN
	if topN <= 0 {
		topN = 20
	}
	if len(result) > topN {
		result = result[:topN]
	}

	return result
}

// Step 7: LLM Rerank
func (s *Service) rerank(ctx context.Context, qc QueryContext, candidates []candidate) ([]candidate, []Evidence, error) {
	// Re-check KU lifecycle right before reranking — recall
	// happened moments earlier via Bleve, which can lag a DB lifecycle change
	// (docs/impl/v1/retrieval.md 步骤 5, "防扫描间隙状态变更").
	candidates = s.filterCurrentUnits(candidates)

	// Assign candidate_ids
	for i := range candidates {
		candidates[i].candidateID = fmt.Sprintf("c%d", i+1)
	}

	unitIDs := make([]string, len(candidates))
	for i := range candidates {
		unitIDs[i] = candidates[i].unitID
	}
	semanticsByUnit, err := s.store.GetUnitRerankSemantics(unitIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieval: rerank get semantics: %w", err)
	}
	centersByUnit, err := s.store.GetUnitCenters(unitIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieval: rerank get centers: %w", err)
	}
	pointsByUnit, err := s.store.GetPointContentsByUnitIDs(unitIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieval: rerank get points: %w", err)
	}

	// A unit's persisted semantics are used regardless of which extraction
	// prompt version produced them — prompt_version is kept on the row for
	// diagnostics only, not as a completeness gate. Requiring an exact match
	// meant every prompt wording tweak instantly broke rerank for the whole
	// existing corpus until every source was re-extracted; only a genuinely
	// missing row (nothing to feed the judge with at all) is a real
	// integrity problem.
	missingSet := make(map[string]struct{})
	var staleCount int
	for _, c := range candidates {
		semantic, ok := semanticsByUnit[c.unitID]
		if !ok {
			missingSet[c.unitID] = struct{}{}
			continue
		}
		if semantic.PromptVersion != rerank.ExtractPromptVersion {
			staleCount++
		}
	}
	if staleCount > 0 {
		slog.Debug("retrieval: rerank using semantics from an older extraction prompt version", "stale_count", staleCount)
	}
	if len(missingSet) > 0 {
		return nil, nil, fmt.Errorf("retrieval: rerank semantics integrity: missing unit_ids: %s", strings.Join(sortedUnitIDs(missingSet), ", "))
	}

	titleCache := make(map[string]string)
	for _, c := range candidates {
		if _, ok := titleCache[c.sourceID]; !ok {
			title, err := s.store.GetSourceTitle(c.sourceID)
			if err != nil {
				slog.Warn("retrieval: rerank get source title failed", "source_id", c.sourceID, "error", err)
				title = c.sourceID
			}
			titleCache[c.sourceID] = title
		}
	}

	judgeCandidates := make([]rerankJudgeCandidate, 0, len(candidates))
	for _, c := range candidates {
		judgeCandidates = append(judgeCandidates, buildRerankJudgeCandidate(
			c.candidateID, titleCache[c.sourceID], centersByUnit[c.unitID], semanticsByUnit[c.unitID], pointsByUnit[c.unitID]))
	}

	roles, err := s.judgeRerankBatches(ctx, qc, judgeCandidates)
	if err != nil {
		return nil, nil, err
	}

	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		for _, c := range candidates {
			role, ok := roles[c.candidateID]
			if !ok {
				role = "(no role returned)"
			}
			slog.Debug("retrieval: rerank judge role", "unit_id", c.unitID, "candidate_id", c.candidateID, "role", role)
		}
	}

	var kept []candidate
	var filtered []Evidence
	for _, c := range candidates {
		role, ok := roles[c.candidateID]
		if !ok || role == "irrelevant" {
			content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
			if err != nil {
				slog.Warn("retrieval: read filtered candidate content failed", "unit_id", c.unitID, "error", err)
				continue
			}
			ref := SourceRef{SourceID: c.sourceID, LineStart: c.lineStart, LineEnd: c.lineEnd}
			refJSON, _ := json.Marshal(ref)
			filtered = append(filtered, Evidence{
				UnitID:    c.unitID,
				PointID:   c.pointID,
				Content:   content,
				SourceRef: refJSON,
				Role:      RoleIrrelevant,
				Origin:    OriginRerank,
			})
			continue
		}
		c.sourcePaths = []string{role}
		kept = append(kept, c)
	}
	return kept, filtered, nil
}

func sortedUnitIDs(ids map[string]struct{}) []string {
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	return sorted
}

// buildRerankJudgeCandidate assembles the judge's entire view of one
// candidate. center (knowledge_units.center, from unit extraction) and points
// (this KU's knowledge_points) come from two independent extraction passes
// over the same KU — including both lowers the chance that a fact the
// question hinges on is invisible to the judge because one pass dropped it.
func buildRerankJudgeCandidate(candidateID, sourceTitle, center string, semantic rerank.Semantics, points []PointFact) rerankJudgeCandidate {
	judgePoints := make([]rerankJudgePoint, len(points))
	for i, p := range points {
		judgePoints[i] = rerankJudgePoint{Content: p.Content, Type: p.PointType}
	}
	return rerankJudgeCandidate{
		CandidateID:  candidateID,
		SourceTitle:  sourceTitle,
		Center:       center,
		SourceTheme:  semantic.SourceTheme,
		ContentTheme: semantic.ContentTheme,
		Intent:       semantic.Intent,
		Object:       semantic.Object,
		Scope:        semantic.Scope,
		Points:       judgePoints,
	}
}

func (s *Service) judgeRerankBatches(ctx context.Context, qc QueryContext, candidates []rerankJudgeCandidate) (map[string]string, error) {
	batches := splitRerankJudgeBatches(candidates, s.rerankJudgeBatchMaxChars())
	if len(batches) == 0 {
		return nil, nil
	}
	// Resolved once per request (not per batch) since it's the same choice
	// for every batch of this call — see rerankJudgePromptFile.
	promptFile := s.rerankJudgePromptFile()
	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}
	audience := qc.Audience
	if audience == "" {
		audience = "（未提取）"
	}
	constraint := qc.Constraint
	if constraint == "" {
		constraint = "（无）"
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	concurrency := s.rerankJudgeConcurrency()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	roles := make(map[string]string)
	var firstErr error
	var errOnce sync.Once

launchBatches:
	for _, batch := range batches {
		batch := batch
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launchBatches
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			batchRoles, err := s.judgeExtractedEvidence(ctx, qc, promptFile, subject, intent, audience, constraint, batch)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			mu.Lock()
			for cid, role := range batchRoles {
				roles[cid] = role
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return roles, nil
}

func (s *Service) judgeExtractedEvidence(ctx context.Context, qc QueryContext, promptFile, subject, intent, audience, constraint string, candidates []rerankJudgeCandidate) (map[string]string, error) {
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge payload: %w", err)
	}
	slog.Debug("retrieval: rerank judge payload", "payload", string(payload))
	resp, err := s.llmClient.CompleteJSON(ctx, promptFile, map[string]string{
		"question":   qc.Question,
		"subject":    subject,
		"intent":     intent,
		"audience":   audience,
		"constraint": constraint,
		"candidates": string(payload),
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge llm: %w", err)
	}

	var result rerankJudgeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge parse: %w", err)
	}

	cidSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		cidSet[c.CandidateID] = true
	}
	roles := make(map[string]string, len(result.Results))
	for _, r := range result.Results {
		if !cidSet[r.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank judge returned unknown candidate_id: %s", r.CandidateID)
		}
		if r.Role != "direct" && r.Role != "supporting" && r.Role != "irrelevant" {
			return nil, fmt.Errorf("retrieval: rerank judge invalid role: %s", r.Role)
		}
		slog.Debug("retrieval: rerank judge analysis", "candidate_id", r.CandidateID, "role", r.Role, "analysis", r.Analysis)
		roles[r.CandidateID] = r.Role
	}
	return roles, nil
}

func (s *Service) rerankJudgeBatchMaxChars() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankJudgeBatchMaxChars > 0 {
		return s.cfg.Retrieval.RerankJudgeBatchMaxChars
	}
	return defaultRerankJudgeBatchMaxChars
}

func (s *Service) rerankJudgeConcurrency() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankJudgeConcurrency > 0 {
		return s.cfg.Retrieval.RerankJudgeConcurrency
	}
	return defaultRerankJudgeConcurrency
}

// rerankJudgeIncludeAnalysis resolves config.Retrieval.RerankJudgeIncludeAnalysis.
// nil (key absent from config.yml) means "unset", which keeps the historical
// behavior (true / include analysis) rather than silently flipping to false —
// a plain bool couldn't distinguish "absent" from "explicitly false" here.
func (s *Service) rerankJudgeIncludeAnalysis() bool {
	if s.cfg == nil || s.cfg.Retrieval.RerankJudgeIncludeAnalysis == nil {
		return true
	}
	return *s.cfg.Retrieval.RerankJudgeIncludeAnalysis
}

// rerankJudgePromptFile picks between the two rerank_judge prompt variants
// based on rerankJudgeIncludeAnalysis: rerank_judge_no_analysis.md drops the
// per-candidate `analysis` explanation (debug-log-only, not decision logic)
// from both the model's output contract and the local Schema validation, to
// A/B test whether the extra generated text is a meaningful latency cost.
func (s *Service) rerankJudgePromptFile() string {
	if s.rerankJudgeIncludeAnalysis() {
		return "rerank_judge.md"
	}
	return "rerank_judge_no_analysis.md"
}

func splitRerankJudgeBatches(candidates []rerankJudgeCandidate, maxChars int) [][]rerankJudgeCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankJudgeBatchMaxChars
	}

	var batches [][]rerankJudgeCandidate
	var current []rerankJudgeCandidate
	currentChars := 2 // JSON array brackets.
	for _, c := range candidates {
		itemJSON, _ := json.Marshal(c)
		itemChars := utf8.RuneCount(itemJSON)
		separatorChars := 0
		if len(current) > 0 {
			separatorChars = 1
		}
		if len(current) > 0 && currentChars+separatorChars+itemChars > maxChars {
			batches = append(batches, current)
			current = nil
			currentChars = 2
			separatorChars = 0
		}
		current = append(current, c)
		currentChars += separatorChars + itemChars
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

type rerankJudgeCandidate struct {
	CandidateID  string             `json:"candidate_id"`
	SourceTitle  string             `json:"source_title"`
	Center       string             `json:"center"`
	SourceTheme  string             `json:"source_theme"`
	ContentTheme string             `json:"content_theme"`
	Intent       string             `json:"intent"`
	Object       string             `json:"object"`
	Scope        string             `json:"scope"`
	Points       []rerankJudgePoint `json:"points"`
}

type rerankJudgePoint struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

type rerankJudgeResult struct {
	Results []struct {
		CandidateID string `json:"candidate_id"`
		Role        string `json:"role"`
		Analysis    string `json:"analysis"`
	} `json:"results"`
}

// Step 8: KPN expansion
// Returns updated candidates (with supporting neighbors) and conflict candidates separately.
func (s *Service) kpnExpand(candidates []candidate) ([]candidate, []candidate, error) {
	var directUnitIDs []string
	for _, c := range candidates {
		if len(c.sourcePaths) > 0 && c.sourcePaths[0] == "direct" {
			directUnitIDs = append(directUnitIDs, c.unitID)
		}
	}
	if len(directUnitIDs) == 0 {
		return candidates, nil, nil
	}

	points, err := s.store.GetPointsByUnitIDs(directUnitIDs)
	if err != nil {
		return nil, nil, err
	}
	seedPointIDs := make([]string, len(points))
	for i, p := range points {
		seedPointIDs[i] = p.PointID
	}

	seedSet := make(map[string]bool, len(seedPointIDs))
	for _, pid := range seedPointIDs {
		seedSet[pid] = true
	}

	existingUnits := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		existingUnits[c.unitID] = true
	}

	// Non-contradicts neighbors → supporting
	neighbors, err := s.store.GetKPNNeighbors(seedPointIDs)
	if err != nil {
		return nil, nil, err
	}

	for _, n := range neighbors {
		if seedSet[n.NeighborPointID] || existingUnits[n.UnitID] {
			continue
		}
		existingUnits[n.UnitID] = true

		u, err := s.store.GetUnitByID(n.UnitID)
		if err != nil {
			slog.Warn("retrieval: kpn expand unit lookup failed", "unit_id", n.UnitID, "error", err)
			continue
		}

		candidates = append(candidates, candidate{
			unitID:      n.UnitID,
			pointID:     n.NeighborPointID,
			sourceID:    u.SourceID,
			lineStart:   u.LineStart,
			lineEnd:     u.LineEnd,
			sourcePaths: []string{"supporting"},
			origin:      OriginKPNExpansion,
		})
	}

	// Contradicts neighbors → conflicts (separate, not mixed into candidates)
	conflicts, err := s.store.GetKPNConflicts(seedPointIDs)
	if err != nil {
		return nil, nil, err
	}

	var conflictCandidates []candidate
	for _, n := range conflicts {
		if seedSet[n.NeighborPointID] {
			continue
		}

		u, err := s.store.GetUnitByID(n.UnitID)
		if err != nil {
			slog.Warn("retrieval: kpn conflict unit lookup failed", "unit_id", n.UnitID, "error", err)
			continue
		}

		conflictCandidates = append(conflictCandidates, candidate{
			unitID:    n.UnitID,
			pointID:   n.NeighborPointID,
			sourceID:  u.SourceID,
			lineStart: u.LineStart,
			lineEnd:   u.LineEnd,
		})
	}

	if len(conflictCandidates) > 0 {
		slog.Info("retrieval: kpn found contradicts", "count", len(conflictCandidates))
	}

	return candidates, conflictCandidates, nil
}

// Step 10: Build EvidenceSet
// buildEvidenceSet reads each candidate's KU text, runs it through the
// Evidence Mining module (docs/impl/v1/evidence.md — fragment-level
// EvidenceItems when mining succeeds, whole-segment passthrough with
// mined=false when it doesn't), and assembles the resulting items into the
// final EvidenceSet (docs/impl/v1/retrieval.md 步骤 3-4). Mining runs once
// here for both the fast and slow paths, since both funnel through this
// function.
func (s *Service) buildEvidenceSet(ctx context.Context, question, subject, intent, audience, constraint, path string, direct, supporting, conflicts []candidate, progress ProgressFunc, lastResort bool) (*EvidenceSet, error) {
	emit := func(phase, status, detail string, dur int64) {
		if progress != nil {
			progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
		}
	}

	es := &EvidenceSet{
		Question:       question,
		Subject:        subject,
		Intent:         intent,
		Audience:       audience,
		Constraint:     constraint,
		Path:           path,
		PathType:       PathTypeFull,
		ActivationHits: []ActivationHit{},
		DirectEvidence: []Evidence{},
		Supporting:     []Evidence{},
	}

	unitSourceIDs := make(map[string]string, len(direct)+len(supporting))
	for _, c := range append(append([]candidate{}, direct...), supporting...) {
		unitSourceIDs[c.unitID] = c.sourceID
	}
	unitIDs := make([]string, 0, len(unitSourceIDs))
	for unitID := range unitSourceIDs {
		unitIDs = append(unitIDs, unitID)
	}
	sort.Strings(unitIDs)
	semantics, err := s.store.GetUnitRerankSemantics(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: get evidence semantics: %w", err)
	}
	sourceTitles := make(map[string]string)
	for _, sourceID := range unitSourceIDs {
		if _, ok := sourceTitles[sourceID]; ok {
			continue
		}
		title, err := s.store.GetSourceTitle(sourceID)
		if err != nil {
			return nil, fmt.Errorf("retrieval: get evidence source title: %w", err)
		}
		sourceTitles[sourceID] = title
	}

	var items []evidence.EvidenceItem
	appendItems := func(cands []candidate, role string) {
		for _, c := range cands {
			content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
			if err != nil {
				slog.Warn("retrieval: read candidate content failed", "unit_id", c.unitID, "role", role, "error", err)
				continue
			}
			origin := c.origin
			if origin == "" {
				origin = OriginRerank
			}
			items = append(items, evidence.EvidenceItem{
				UnitID: c.unitID, PointID: c.pointID, SourceID: c.sourceID,
				LineStart: c.lineStart, LineEnd: c.lineEnd,
				Content: content, Role: role, Origin: origin,
			})
		}
	}
	appendItems(direct, evidence.RoleDirect)
	appendItems(supporting, evidence.RoleSupporting)

	emit("evidence", "start", fmt.Sprintf("%d 条候选", len(items)), 0)
	evidenceStart := time.Now()
	mined := items
	if s.evidenceSvc != nil {
		mined = s.evidenceSvc.Mine(ctx, question, subject, intent, items, lastResort)
	}
	minedCount := 0
	for _, item := range mined {
		if item.Mined {
			minedCount++
		}
	}
	emit("evidence", "done", fmt.Sprintf("%d/%d 条片段化", minedCount, len(mined)), time.Since(evidenceStart).Milliseconds())

	for _, item := range mined {
		ref := SourceRef{SourceID: item.SourceID, LineStart: item.LineStart, LineEnd: item.LineEnd}
		refJSON, _ := json.Marshal(ref)
		semantic := semantics[item.UnitID]
		ev := Evidence{
			FactID:       uuid.New().String(),
			UnitID:       item.UnitID,
			PointID:      item.PointID,
			Content:      item.Content,
			SourceRef:    refJSON,
			Role:         item.Role,
			Origin:       item.Origin,
			SourceTitle:  sourceTitles[item.SourceID],
			SourceTheme:  semantic.SourceTheme,
			ContentTheme: semantic.ContentTheme,
			Object:       semantic.Object,
			Scope:        semantic.Scope,
			Mined:        item.Mined,
		}
		switch item.Role {
		case evidence.RoleDirect:
			es.DirectEvidence = append(es.DirectEvidence, ev)
		case evidence.RoleSupporting:
			es.Supporting = append(es.Supporting, ev)
		}
	}

	for _, c := range conflicts {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err != nil {
			slog.Warn("retrieval: build conflict evidence failed", "unit_id", c.unitID, "error", err)
			continue
		}

		ref := SourceRef{
			SourceID:  c.sourceID,
			LineStart: c.lineStart,
			LineEnd:   c.lineEnd,
		}
		refJSON, _ := json.Marshal(ref)

		title, _ := s.store.GetSourceTitle(c.sourceID)

		es.Conflicts = append(es.Conflicts, ConflictEvidence{
			UnitID:      c.unitID,
			PointID:     c.pointID,
			Content:     content,
			SourceRef:   refJSON,
			SourceTitle: title,
		})
	}

	return es, nil
}

// filterCurrentUnits drops candidates whose KU is no longer lifecycle=current.
func (s *Service) filterCurrentUnits(candidates []candidate) []candidate {
	kept := make([]candidate, 0, len(candidates))
	checked := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		current, ok := checked[c.unitID]
		if !ok {
			var err error
			current, err = s.store.UnitLifecycleCurrent(c.unitID)
			if err != nil {
				slog.Warn("retrieval: rerank lifecycle re-check failed, keeping candidate", "unit_id", c.unitID, "error", err)
				current = true
			}
			checked[c.unitID] = current
		}
		if current {
			kept = append(kept, c)
		} else {
			slog.Info("retrieval: rerank dropped candidate (KU no longer current)", "unit_id", c.unitID)
		}
	}
	return kept
}

func (s *Service) readUnitContent(sourceID string, lineStart, lineEnd int) (string, error) {
	mdPath, err := s.store.GetSourceMarkdownPath(sourceID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("retrieval: read markdown %s: %w", mdPath, err)
	}
	lines := strings.Split(string(data), "\n")
	return sliceLines(lines, lineStart, lineEnd), nil
}

func sliceLines(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	if lineStart > lineEnd {
		return ""
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}

func intFromField(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	default:
		return 0
	}
}

// extractQuestionNouns extracts key entity phrases from the question.
// It splits on punctuation and particles, returning short phrases (2-8 chars)
// that are likely entity names (place, document, product, etc).
func extractQuestionNouns(question string) []string {
	// 多字疑问词分隔符必须排在单字分隔符（如"是"）之前，否则会被单字分隔符提前
	// 截断（如"是否"会被"是"拆成 ""+"否"，残留出"否..."这种无效片段）。
	splitters := []string{
		"如何", "是否", "能否", "怎么样", "怎么", "怎样", "为什么", "什么", "多少", "哪些",
		"，", "。", "？", "?", "！", "、", "：", "的", "中", "呢", "吗", "了", "是",
	}
	parts := []string{question}
	for _, sep := range splitters {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}

	// Also split each part by spaces
	var segments []string
	for _, p := range parts {
		for _, s := range strings.Fields(p) {
			s = strings.TrimSpace(s)
			r := []rune(s)
			if len(r) >= 2 && len(r) <= 8 {
				segments = append(segments, s)
			}
		}
	}

	// Filter out common verb/question words that are not entities
	commonWords := map[string]bool{
		"什么": true, "哪些": true, "怎么": true, "如何": true, "多少": true,
		"怎样": true, "为什么": true, "是否": true, "能否": true,
		"查询": true, "查看": true, "帮我": true, "看看": true, "分析": true,
		"标准": true, "怎么样": true, "是多少": true, "有哪些": true,
	}
	var nouns []string
	for _, s := range segments {
		if !commonWords[s] {
			nouns = append(nouns, s)
		}
	}
	return nouns
}

func containsAnyNoun(content string, nouns []string) bool {
	for _, n := range nouns {
		if strings.Contains(content, n) {
			return true
		}
	}
	return false
}

// lifecycleCurrentQuery conjoins base with a lifecycle=current TermQuery
// (docs/impl/v1/retrieval.md 步骤 5) so superseded/deprecated KU/KP never
// surface from the units/points Bleve indexes.
func lifecycleCurrentQuery(base query.Query) query.Query {
	lq := bleve.NewTermQuery("current")
	lq.SetField("lifecycle")
	return bleve.NewConjunctionQuery(base, lq)
}

// buildSourceIDQuery creates a Bleve boolean query to filter by source IDs.
func buildSourceIDQuery(sourceIDs []string) query.Query {
	if len(sourceIDs) == 0 {
		return nil
	}
	disjuncts := make([]query.Query, len(sourceIDs))
	for i, sid := range sourceIDs {
		tq := bleve.NewTermQuery(sid)
		tq.SetField("source_id")
		disjuncts[i] = tq
	}
	return bleve.NewDisjunctionQuery(disjuncts...)
}
