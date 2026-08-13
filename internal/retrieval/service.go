package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
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
	store              *Store
	llmClient          llm.LLMClient
	unitsIndex         bleve.Index
	pointsIndex        bleve.Index
	outlinesIndex      bleve.Index
	cfg                *config.Config
	activationSvc      *activation.Service
	evidenceSvc        *evidence.Service
	wikiSvc            *wiki.Service
	auditWriter        AuditOutcomeWriter
	synthesisWriter    SynthesisOutcomeWriter
	synthesisRandFloat func() float64
}

// AuditOutcomeWriter is implemented by trace.Service — mirrors the
// cross-package notification interface shape already used elsewhere in this
// codebase (unit.ActivationNotifier, activation.WikiNotifier): the interface
// is defined in the consumer package (retrieval), the producer package
// (trace) satisfies it structurally, and main.go wires the two together with
// a setter. It's the hand-off target for the independent-verification audit
// trial (docs/impl/v1/retrieval.md 步骤 2c) — Retrieval triggers the trial and
// hands the independently-derived comparison inputs to Trace, which owns the
// comparison-outcome bookkeeping (activation_audit_success/failure events +
// activation.RecordAuditOutcome, docs/impl/v1/trace.md 步骤 3b).
type AuditOutcomeWriter interface {
	WriteAuditOutcome(linkID, pointID, subject, intent, audience, constraint string, agree bool, slowPathDirectPointIDs []string) error
}

// SetAuditOutcomeWriter wires the audit-trial hand-off target (usually
// *trace.Service). Unset means audit trials never fire — nil-safe, mirrors
// how other optional notifier fields in this codebase are called
// defensively.
func (s *Service) SetAuditOutcomeWriter(w AuditOutcomeWriter) {
	s.auditWriter = w
}

// SynthesisOutcomeWriter is implemented by *trace.Service — mirrors
// AuditOutcomeWriter's shape, the hand-off target for Wiki's
// synthesis-satisfaction axis independent-verification trial
// (docs/impl/v1/wiki.md 步骤 4a, reusing retrieval.md 步骤 2c's exact
// orchestration). Retrieval triggers the trial (it already has both wikiSvc
// and the slow-path retrieval wiki.Service can't reach without an import
// cycle) and hands the comparison to Trace, which owns the event/counter
// bookkeeping.
type SynthesisOutcomeWriter interface {
	WriteSynthesisOutcome(pageID, auditedTraceQuestion string, slowPathDirectPointIDs []string, agree bool) error
}

// SetSynthesisOutcomeWriter wires the synthesis-audit-trial hand-off target
// (usually *trace.Service). Unset means synthesis audit trials never fire —
// nil-safe, mirrors SetAuditOutcomeWriter.
func (s *Service) SetSynthesisOutcomeWriter(w SynthesisOutcomeWriter) {
	s.synthesisWriter = w
}

const (
	defaultRerankJudgeBatchMaxChars = 4000
	defaultRerankJudgeConcurrency   = 4
)

func NewService(store *Store, llmClient llm.LLMClient, unitsIdx, pointsIdx, outlinesIdx bleve.Index, cfg *config.Config, activationSvc *activation.Service, evidenceSvc *evidence.Service, wikiSvc *wiki.Service) *Service {
	return &Service{
		store:              store,
		llmClient:          llmClient,
		unitsIndex:         unitsIdx,
		pointsIndex:        pointsIdx,
		outlinesIndex:      outlinesIdx,
		cfg:                cfg,
		activationSvc:      activationSvc,
		evidenceSvc:        evidenceSvc,
		wikiSvc:            wikiSvc,
		synthesisRandFloat: rand.Float64,
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

	// docs/impl/v1/wiki.md 步骤 4a: after a Wiki direct answer has been
	// successfully served, sample synthesis_audit_rate and, on a hit, launch
	// a detached background independent-verification trial — same
	// "async, never blocks the response" shape as launchAuditTrials (步骤 2c).
	s.launchSynthesisAuditTrial(ctx, qc, result.PageID)

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
	// 问题四元组归一化（2026-08-12 新增，config.Retrieval.QuestionTupleNormEnabled
	// 门控，默认关闭）：session_normalize_tuple 曾经在这里做过一次盲改四元组再
	// 赌重新匹配的二次规范化调用，已于 2026-08-12 废弃；这里恢复的是一个不同的
	// 机制——四层递进（精确匹配 → 本地词集 Jaccard 相似度 → 向量早筛，仅拒绝不
	// 单独确认 → LLM 批量判断），只在前两层免费的程序判断都未命中、且向量层
	// （若启用）未提前拒绝时，才在双重未命中的情况下多付一次 LLM 调用。三个消
	// 费入口（activation.Matcher/BundleMatcher、wiki.matchFourTupleEntry）本身
	// 仍是纯精确匹配，不受影响——这里只改变喂给它们的四元组。详见
	// docs/impl/v1/retrieval.md 步骤 2。
	workQC := qc
	if s.cfg.Retrieval.QuestionTupleNormEnabled && qc.DomainResolved && len(qc.DomainIDs) > 0 {
		normSubject, normIntent, normAudience, normConstraint, err := s.activationSvc.NormalizeTuple(ctx, qc.DomainIDs, qc.Subject, qc.Intent, qc.Audience, qc.Constraint)
		if err != nil {
			slog.Warn("retrieval: question tuple normalization failed, using raw session tuple", "error", err)
		} else {
			workQC.Subject = normSubject
			workQC.Intent = normIntent
			workQC.Audience = normAudience
			workQC.Constraint = normConstraint
		}
	}
	expandedQuery := session.ExpandedQuery{
		ExpandedQuestion: workQC.Question,
		Subject:          workQC.Subject,
		Intent:           workQC.Intent,
		Audience:         workQC.Audience,
		Constraint:       workQC.Constraint,
	}
	matches, err := s.activationSvc.Match(ctx, expandedQuery, matchCfg)
	if err != nil {
		slog.Warn("retrieval: activation match failed, falling back to slow path", "error", err)
		return nil, nil, false
	}
	matches = s.filterMatchesByDomain(matches, workQC)
	if len(matches) == 0 {
		return nil, nil, false
	}

	activationHits, verified, ok := classifyActivationMatches(matches)
	if !ok {
		return nil, activationHits, false
	}

	linkIDs, pointIDs := verifiedIDs(verified)

	hits, unitStatus := s.resolveUnitsForPoints(pointIDs, linkIDs)
	switch unitStatus {
	case unitResolutionFailed:
		return nil, activationHits, false
	case unitResolutionAmbiguous:
		bundleHits, bundleOK := s.resolveBundleForAmbiguousHits(ctx, workQC, expandedQuery, matchCfg, linkIDs, pointIDs)
		if !bundleOK {
			return nil, activationHits, false
		}
		hits = bundleHits
	}

	// Async, non-blocking — touch every verified link that contributed to the
	// resolved hit (same KU, or Bundle-resolved across units).
	go func() {
		if err := s.activationSvc.TouchLastUsed(linkIDs); err != nil {
			slog.Warn("retrieval: touch last used failed", "error", err)
		}
	}()

	es, resultHits, ok := s.finishFastPath(ctx, workQC, hits, linkIDs, activationHits)
	if ok {
		// docs/impl/v1/retrieval.md 步骤 2c: fire after the fast-path answer is
		// fully assembled and about to be handed back — non-blocking, same
		// "async, detached from the response path" shape as the TouchLastUsed
		// goroutine above, just triggered from the caller's return point
		// instead of before it since audit sampling is decided per-hit
		// (activationHits, not just the linkIDs that resolved a KU).
		s.launchAuditTrials(workQC, resultHits)
	}
	return es, resultHits, ok
}

// launchAuditTrials implements docs/impl/v1/retrieval.md 步骤 2c: for every
// hit Match() marked AuditSampled=true, run an independent slow-path
// retrieval in the background and hand the comparison to auditWriter. Never
// blocks or delays the caller — each trial is its own goroutine, detached
// from the request's context (a canceled request context would otherwise
// abort the trial the instant the HTTP response finishes).
func (s *Service) launchAuditTrials(qc QueryContext, hits []ActivationHit) {
	if s.auditWriter == nil {
		return
	}
	for _, h := range hits {
		if !h.AuditSampled {
			continue
		}
		hit := h
		go s.runAuditTrial(qc, hit)
	}
}

// runAuditTrial runs one independent-verification trial: a full forced
// slow-path retrieval (reusing RetrieveSlowPathWithProgress as-is — no new
// prompt, no new LLM call type, per docs/impl/v1/retrieval.md 步骤 2c's
// "复用已有的慢路径，不新增 prompt"), then compares its independently-derived
// DirectEvidence point_ids against the fast-path hit's point_id. Any failure
// running the slow path itself (error, timeout) is logged and dropped —
// "宁可少一次审计样本，也不能把一次基础设施故障误记成独立核实的否定结论" — no
// WriteAuditOutcome call happens in that case.
func (s *Service) runAuditTrial(qc QueryContext, hit ActivationHit) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	es, err := s.RetrieveSlowPathWithProgress(ctx, qc, nil)
	if err != nil {
		slog.Warn("retrieval: audit trial slow path failed, dropping sample",
			"link_id", hit.LinkID, "point_id", hit.PointID, "error", err)
		return
	}

	var slowPathDirectPointIDs []string
	for _, ev := range es.DirectEvidence {
		slowPathDirectPointIDs = append(slowPathDirectPointIDs, ev.PointID)
	}
	agree := false
	for _, pid := range slowPathDirectPointIDs {
		if pid == hit.PointID {
			agree = true
			break
		}
	}

	if err := s.auditWriter.WriteAuditOutcome(hit.LinkID, hit.PointID, hit.Subject, hit.Intent, hit.Audience, hit.Constraint, agree, slowPathDirectPointIDs); err != nil {
		slog.Warn("retrieval: write audit outcome failed", "link_id", hit.LinkID, "point_id", hit.PointID, "error", err)
	}
}

// launchSynthesisAuditTrial implements docs/impl/v1/wiki.md 步骤 4a: after a
// Wiki direct answer has been served, sample wiki.synthesis_audit_rate and,
// on a hit, run the trial in a detached background goroutine — never blocks
// or delays the caller, mirroring launchAuditTrials/runAuditTrial exactly.
func (s *Service) launchSynthesisAuditTrial(ctx context.Context, qc QueryContext, pageID string) {
	if s.synthesisWriter == nil || pageID == "" {
		return
	}
	rate := s.cfg.Wiki.SynthesisAuditRate
	if rate <= 0 {
		return
	}
	randFloat := s.synthesisRandFloat
	if randFloat == nil {
		randFloat = rand.Float64
	}
	if randFloat() >= rate {
		return
	}
	go s.runSynthesisAuditTrial(qc, pageID)
}

// runSynthesisAuditTrial runs one independent-verification trial for the
// synthesis-satisfaction axis: a full forced slow-path retrieval (reusing
// RetrieveSlowPathWithProgress as-is, same "复用已有的慢路径，不新增 prompt"
// discipline as runAuditTrial), then checks whether its independently-derived
// DirectEvidence point_ids intersect the served page's source_point_ids
// (docs/impl/v1/wiki.md 步骤 4a's exact comparison — page scope, not a single
// point_id match like ActivationLink/Bundle's audit). Any failure running the
// slow path itself, or reading the page, is logged and dropped — no
// WriteSynthesisOutcome call happens in that case.
func (s *Service) runSynthesisAuditTrial(qc QueryContext, pageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if s.wikiSvc == nil {
		return
	}
	page, err := s.wikiSvc.GetPage(pageID)
	if err != nil || page == nil {
		slog.Warn("retrieval: synthesis audit trial page lookup failed, dropping sample", "page_id", pageID, "error", err)
		return
	}
	var scopeIDs []string
	if err := json.Unmarshal([]byte(page.SourcePointIDs), &scopeIDs); err != nil {
		slog.Warn("retrieval: synthesis audit trial page source_point_ids unmarshal failed, dropping sample", "page_id", pageID, "error", err)
		return
	}
	scope := make(map[string]bool, len(scopeIDs))
	for _, pid := range scopeIDs {
		scope[pid] = true
	}

	es, err := s.RetrieveSlowPathWithProgress(ctx, qc, nil)
	if err != nil {
		slog.Warn("retrieval: synthesis audit trial slow path failed, dropping sample", "page_id", pageID, "error", err)
		return
	}

	var slowPathDirectPointIDs []string
	agree := false
	for _, ev := range es.DirectEvidence {
		slowPathDirectPointIDs = append(slowPathDirectPointIDs, ev.PointID)
		if scope[ev.PointID] {
			agree = true
		}
	}

	if err := s.synthesisWriter.WriteSynthesisOutcome(pageID, qc.Question, slowPathDirectPointIDs, agree); err != nil {
		slog.Warn("retrieval: write synthesis outcome failed", "page_id", pageID, "error", err)
	}
}

// finishFastPath implements docs/impl/v1/retrieval.md 步骤 2's shared
// candidate-building → KPN expand → evidence build → verify tail, reused by
// both the single-unit hit path and the Bundle-resolved (possibly
// multi-unit) hit path — this block never assumed single-unit internally, it
// only ever received a single-unit hits list because the caller gated on it
// upstream.
func (s *Service) finishFastPath(ctx context.Context, workQC QueryContext, hits []DirectHit, linkIDs []string, activationHits []ActivationHit) (*EvidenceSet, []ActivationHit, bool) {
	if !s.cfg.Retrieval.FastPath {
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
	allCandidates, _, err = s.judgeKPNExpansion(ctx, workQC, allCandidates)
	if err != nil {
		slog.Warn("retrieval: fast path judge kpn expansion failed, falling back to slow path", "error", err)
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

	es, err := s.buildEvidenceSet(ctx, workQC.Question, workQC.Subject, workQC.Intent, workQC.Audience, workQC.Constraint, "short", directCands, supportingCands, conflictCandidates, nil, false)
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
		sufficient, needsDeep, _, err := s.VerifyEvidenceSufficient(ctx, workQC.Question, es)
		if err != nil {
			slog.Warn("retrieval: fast path verify failed, falling back to slow path", "error", err)
			return nil, activationHits, false
		}
		if !sufficient {
			slog.Info("retrieval: fast path verify judged evidence insufficient, falling back to slow path", "link_ids", linkIDs)
			return nil, activationHits, false
		}
		if needsDeep {
			// Question complexity doesn't depend on which retrieval path
			// found the evidence — fast path only means "found cheaply via
			// activation links", not "answer with a single shallow pass".
			// Keep the fast-path evidence and just answer it with the deep
			// prompt instead of discarding a good activation hit and paying
			// for a full slow-path re-retrieval.
			es.Path = "deep"
			slog.Info("retrieval: fast path verify judged needs_deep, answering deep on fast-path evidence", "link_ids", linkIDs)
		}
	}

	es.PathType = PathTypeFast
	es.ActivationHits = activationHits
	slog.Info("retrieval: fast path evidence built",
		"direct", len(es.DirectEvidence), "supporting", len(es.Supporting), "link_ids", linkIDs)
	return es, activationHits, true
}

// SlowPathVerifyEnabled reports whether Answer should run the slow-path
// sufficiency gate (docs/impl/v1/retrieval.md 步骤 2b) before generating.
func (s *Service) SlowPathVerifyEnabled() bool {
	return s.cfg != nil && s.cfg.Retrieval.SlowPathVerify
}

// VerifyEvidenceSufficient implements docs/impl/v1/retrieval.md 步骤 2a/2b:
// a single LLM call judging whether the assembled evidence independently
// and completely answers the question. Used by the fast path (before
// committing PathType=fast) and by Answer on PathType=full (before
// generation). Any ambiguity (LLM error, unparseable response,
// sufficient=false) is reported to the caller — fast path treats that as
// failure and falls back; slow path refuses on sufficient=false and
// proceeds on call/parse errors.
//
// The same call also returns needsDeep: sufficient=true evidence can still
// require resolving an intermediate fact from the evidence (a
// classification/lookup) before the stated rule applies correctly — see
// fast_verify.md 第三步. needsDeep is only meaningful when sufficient=true.
func (s *Service) VerifyEvidenceSufficient(ctx context.Context, question string, es *EvidenceSet) (sufficient bool, needsDeep bool, reason string, err error) {
	// Object/scope/theme are included alongside content (not just
	// SourceTitle+Content) so this independently-framed second pass can
	// cross-check the direct evidence's stated object/scenario against the
	// question's audience/constraint — a check rerank_judge already attempts
	// per-candidate but sometimes gets wrong; this call uses a different task
	// framing (completeness, not per-candidate classification) over the same
	// underlying fields, so its errors aren't perfectly correlated with
	// rerank_judge's.
	var evidenceText strings.Builder
	for i, ev := range es.DirectEvidence {
		fmt.Fprintf(&evidenceText, "[direct-%d] （来源：%s | 来源归属：%s | 内容主题：%s | 对象：%s | 范围：%s）%s\n",
			i+1, ev.SourceTitle, ev.SourceTheme, ev.ContentTheme, ev.Object, ev.Scope, ev.Content)
	}
	for i, ev := range es.Supporting {
		fmt.Fprintf(&evidenceText, "[supporting-%d] （来源：%s | 来源归属：%s | 内容主题：%s | 对象：%s | 范围：%s）%s\n",
			i+1, ev.SourceTitle, ev.SourceTheme, ev.ContentTheme, ev.Object, ev.Scope, ev.Content)
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "fast_verify.md", map[string]string{
		"question":   question,
		"audience":   es.Audience,
		"constraint": es.Constraint,
		"evidence":   evidenceText.String(),
		"has_direct": fmt.Sprintf("%v", len(es.DirectEvidence) > 0),
	}, "classification")
	if err != nil {
		return false, false, "", fmt.Errorf("retrieval: evidence verify call: %w", err)
	}

	var result struct {
		Sufficient bool   `json:"sufficient"`
		NeedsDeep  bool   `json:"needs_deep"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return false, false, "", fmt.Errorf("retrieval: parse evidence verify response: %w", err)
	}
	if !result.Sufficient {
		slog.Info("retrieval: evidence verify judged insufficient", "reason", result.Reason)
		result.NeedsDeep = false
	} else if result.NeedsDeep {
		slog.Info("retrieval: evidence verify judged needs_deep", "reason", result.Reason)
	}
	return result.Sufficient, result.NeedsDeep, result.Reason, nil
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
	candidateSources, err := s.domainPreFilter(ctx, qc)
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
		emit("screen", "done", "0 条", 0)
		emit("rerank", "done", "0 直接 · 0 间接", 0)
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
//
// Progress is split into two UI phases when possible: "screen"（证据筛选 /
// relevance）then "rerank"（证据分类 / direct·supporting + KPN expansion）.
func (s *Service) rerankAndBuildEvidenceSet(ctx context.Context, qc QueryContext, merged []candidate, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool) (*EvidenceSet, error) {
	question := qc.Question

	// Step 7: 证据筛选 + 证据分类（LLM Rerank）。classifyStart 在收到
	// rerank/start 时重置，done 耗时覆盖 classify + 后续 KPN 扩展。
	classifyStart := time.Now()
	sawRerankStart := false
	progressEmit := func(phase, status, detail string, dur int64) {
		if phase == "rerank" && status == "start" {
			classifyStart = time.Now()
			sawRerankStart = true
		}
		emit(phase, status, detail, dur)
	}
	reranked, filteredEvidence, err := s.rerankWithProgress(ctx, qc, merged, progressEmit)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank: %w", err)
	}
	if !sawRerankStart {
		emit("rerank", "start", "0 条", 0)
		classifyStart = time.Now()
	}
	slog.Info("retrieval: step7 rerank done", "kept", len(reranked), "filtered", len(filteredEvidence))

	// Step 8: KPN expansion
	kpnLookupStart := time.Now()
	reranked, conflictCandidates, err := s.kpnExpand(reranked)
	if err != nil {
		return nil, fmt.Errorf("retrieval: kpn expand: %w", err)
	}
	slog.Info("retrieval: kpn expand db lookup timing", "duration_ms", time.Since(kpnLookupStart).Milliseconds(), "candidates", len(reranked))
	var kpnFiltered []Evidence
	kpnJudgeStart := time.Now()
	reranked, kpnFiltered, err = s.judgeKPNExpansion(ctx, qc, reranked)
	if err != nil {
		return nil, fmt.Errorf("retrieval: judge kpn expansion: %w", err)
	}
	slog.Info("retrieval: kpn expansion judge timing", "duration_ms", time.Since(kpnJudgeStart).Milliseconds())
	filteredEvidence = append(filteredEvidence, kpnFiltered...)

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
	emit("rerank", "done", fmt.Sprintf("%d 直接 · %d 间接", len(direct), len(supporting)), time.Since(classifyStart).Milliseconds())

	// Force whole-segment mining fallback for the induction question shape:
	// no direct evidence at all, and the supporting evidence spans multiple
	// distinct sources — i.e. several category members (docs/impl/v1's
	// "整体/成员归纳" pattern in fast_verify.md), not just multiple
	// fragments of the same source. evidence_mine.md's "nothing to mine"
	// verdict for a supporting candidate is otherwise only rescued via
	// whole-segment fallback on the caller's last-resort retry (see
	// mineBatch's role==RoleSupporting branch) — everywhere else that
	// verdict silently drops the candidate. For this shape, a member losing
	// its only evidence isn't a quality tradeoff, it can flip the
	// sufficiency verdict entirely (one fewer member covered), so it's
	// forced here on the first attempt rather than waiting for a retry that
	// may never come.
	if !lastResort && len(direct) == 0 && len(supporting) > 0 {
		distinctSources := make(map[string]bool, len(supporting))
		for _, c := range supporting {
			distinctSources[c.sourceID] = true
		}
		if len(distinctSources) >= 2 {
			slog.Info("retrieval: forcing last-resort mining fallback for multi-source supporting-only evidence",
				"supporting", len(supporting), "distinct_sources", len(distinctSources))
			lastResort = true
		}
	}

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

// Step 2: Domain pre-filter. When qc.DomainResolved (session already routed
// domains in the merged parse prompt), reuse DomainIDs and never call
// question_domain_match again — empty DomainIDs means all sources.
func (s *Service) domainPreFilter(ctx context.Context, qc QueryContext) ([]SourceInfo, error) {
	if qc.DomainResolved {
		if len(qc.DomainIDs) == 0 {
			slog.Info("retrieval: domain pre-filter using upstream empty domain_ids, all sources")
			return s.store.ListAllSources()
		}
		sources, err := s.store.ListSourcesByDomainIDs(qc.DomainIDs)
		if err != nil {
			return nil, err
		}
		if len(sources) == 0 {
			slog.Warn("retrieval: upstream domain_ids matched zero sources, falling back to all sources", "domain_ids", qc.DomainIDs)
			return s.store.ListAllSources()
		}
		return sources, nil
	}

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
		"question":    qc.Question,
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

	outlineBoost := s.cfg.Retrieval.OutlineRRFBoost
	if outlineBoost <= 0 {
		outlineBoost = 1.0
	}

	addRanked := func(candidates []candidate, pathName string) {
		sorted := append([]candidate(nil), candidates...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].score != sorted[j].score {
				return sorted[i].score > sorted[j].score
			}
			return sorted[i].unitID < sorted[j].unitID
		})
		for rank, c := range sorted {
			rrfScore := 1.0 / float64(k+rank+1)
			if pathName == "outline" {
				rrfScore *= outlineBoost
			}
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
		sort.Strings(paths)
		m.candidate.score = m.rrfScore
		m.candidate.sourcePaths = paths
		m.candidate.recallOrigins = paths
		result = append(result, m.candidate)
	}

	// Deterministic tie-break on unitID: map iteration order above is
	// randomized per Go's spec, and RRF scores frequently tie at the topN
	// boundary — without a stable secondary key, which candidates survive
	// truncation below would vary between otherwise-identical requests.
	sort.Slice(result, func(i, j int) bool {
		if result[i].score != result[j].score {
			return result[i].score > result[j].score
		}
		return result[i].unitID < result[j].unitID
	})
	for i := range result {
		result[i].mergedRank = i
	}

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
	return s.rerankWithProgress(ctx, qc, candidates, nil)
}

func (s *Service) rerankWithProgress(ctx context.Context, qc QueryContext, candidates []candidate, emit func(phase, status, detail string, dur int64)) ([]candidate, []Evidence, error) {
	// Re-check KU lifecycle right before reranking — recall
	// happened moments earlier via Bleve, which can lag a DB lifecycle change
	// (docs/impl/v1/retrieval.md 步骤 5, "防扫描间隙状态变更").
	candidates = s.filterCurrentUnits(candidates)
	return s.judgeCandidates(ctx, qc, candidates, emit)
}

// judgeCandidates runs the LLM rerank judge over an arbitrary candidate set
// and splits the result into kept (role stashed in sourcePaths[0]) and
// filtered (irrelevant, kept as Evidence for the trail). Shared by rerank()
// (Step 7, all recalled candidates) and kpnExpand's neighbor validation
// (candidates added via a KPN "related" edge, which — unlike Step 7's
// recall — never passed any relevance judgment on their own; a KPN edge
// only says two points are topically related, not that the neighbor fits
// this question's object/scenario, so it must clear the same judge before
// being trusted as supporting evidence).
//
// emit, when non-nil, receives screen/rerank progress for the process panel
// (nil for KPN expansion — those judgments must not overwrite the main-path
// screen/classify steps already shown to the user).
func (s *Service) judgeCandidates(ctx context.Context, qc QueryContext, candidates []candidate, emit func(phase, status, detail string, dur int64)) ([]candidate, []Evidence, error) {
	for i := range candidates {
		candidates[i].candidateID = fmt.Sprintf("c%d", i+1)
	}

	judgeItems, err := s.buildJudgeItems(candidates)
	if err != nil {
		return nil, nil, err
	}

	roles, err := s.judgeRerankTwoStep(ctx, qc, judgeItems, emit)
	if err != nil {
		return nil, nil, err
	}

	kept, filtered := s.splitKeptFiltered(candidates, roles)
	return kept, filtered, nil
}

// buildJudgeItems fetches each candidate's persisted rerank semantics,
// center sentence, knowledge points, and source title, and assembles the
// judge-facing rerankJudgeCandidate view shared by every judge prompt
// variant (combined and two-step split).
func (s *Service) buildJudgeItems(candidates []candidate) ([]rerankJudgeCandidate, error) {
	unitIDs := make([]string, len(candidates))
	for i := range candidates {
		unitIDs[i] = candidates[i].unitID
	}
	semanticsByUnit, err := s.store.GetUnitRerankSemantics(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank get semantics: %w", err)
	}
	centersByUnit, err := s.store.GetUnitCenters(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank get centers: %w", err)
	}
	pointsByUnit, err := s.store.GetPointContentsByUnitIDs(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank get points: %w", err)
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
		return nil, fmt.Errorf("retrieval: rerank semantics integrity: missing unit_ids: %s", strings.Join(sortedUnitIDs(missingSet), ", "))
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

	judgeItems := make([]rerankJudgeCandidate, 0, len(candidates))
	for _, c := range candidates {
		judgeItems = append(judgeItems, buildRerankJudgeCandidate(
			c.candidateID, titleCache[c.sourceID], centersByUnit[c.unitID], semanticsByUnit[c.unitID], pointsByUnit[c.unitID]))
	}
	return judgeItems, nil
}

// splitKeptFiltered applies a role map (candidate_id -> "direct"/
// "supporting"/"irrelevant", missing entries treated as irrelevant) to the
// original candidates, tagging kept ones with their role in sourcePaths[0]
// and reading filtered ones' content into Evidence for the trail.
func (s *Service) splitKeptFiltered(candidates []candidate, roles map[string]string) ([]candidate, []Evidence) {
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
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
	return kept, filtered
}

// judgeCandidatesRelevanceOnly runs just Step 1 (rerank_relevance.md) of the
// two-step judge — used by judgeKPNExpansion, where a classify call would be
// wasted: a KPN-expanded neighbor's role is always coerced to "supporting"
// regardless of what classify would say, so skipping it saves a full LLM
// round trip per expansion batch without changing the outcome.
func (s *Service) judgeCandidatesRelevanceOnly(ctx context.Context, qc QueryContext, candidates []candidate) ([]candidate, []Evidence, error) {
	for i := range candidates {
		candidates[i].candidateID = fmt.Sprintf("c%d", i+1)
	}

	judgeItems, err := s.buildJudgeItems(candidates)
	if err != nil {
		return nil, nil, err
	}

	relevant, err := s.judgeRelevanceBatches(ctx, qc, judgeItems)
	if err != nil {
		return nil, nil, err
	}

	roles := make(map[string]string, len(candidates))
	for cid, ok := range relevant {
		if ok {
			roles[cid] = "supporting"
		} else {
			roles[cid] = "irrelevant"
		}
	}

	kept, filtered := s.splitKeptFiltered(candidates, roles)
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

// judgeRerankTwoStep splits candidate judging into two sequential LLM
// passes. Step 1 (rerank_relevance.md) judges only relevance (relevant/
// irrelevant) — the object/scenario hard gate — as its own narrower task.
// Step 2 (rerank_classify.md) then classifies only the candidates Step 1
// confirmed relevant into direct/supporting, a task that no longer needs to
// re-derive object/scenario matching since Step 1 already guaranteed it.
// This removes a failure mode a single combined judgment was prone to: a
// well-formed but wrong-object candidate (e.g. a complete price table for
// the wrong audience) getting judged direct because, mid-reasoning through
// both relevance and classification at once, its completeness outweighed an
// object mismatch the model had already noticed (2026-08-08 决策: 旁路验证后
// 转正，替换原来的单次 rerank_judge.md 调用).
func (s *Service) judgeRerankTwoStep(ctx context.Context, qc QueryContext, candidates []rerankJudgeCandidate, emit func(phase, status, detail string, dur int64)) (map[string]string, error) {
	if emit == nil {
		emit = func(string, string, string, int64) {}
	}
	emit("screen", "start", fmt.Sprintf("%d 条候选", len(candidates)), 0)
	relevanceStart := time.Now()
	relevant, err := s.judgeRelevanceBatches(ctx, qc, candidates)
	relevanceMs := time.Since(relevanceStart).Milliseconds()
	if err != nil {
		slog.Info("retrieval: rerank two-step relevance timing", "candidates", len(candidates), "duration_ms", relevanceMs, "error", err)
		emit("screen", "error", err.Error(), relevanceMs)
		return nil, fmt.Errorf("retrieval: rerank two-step relevance: %w", err)
	}

	roles := make(map[string]string, len(candidates))
	var relevantItems []rerankJudgeCandidate
	for _, c := range candidates {
		if relevant[c.CandidateID] {
			relevantItems = append(relevantItems, c)
		} else {
			roles[c.CandidateID] = "irrelevant"
		}
	}
	slog.Info("retrieval: rerank two-step relevance timing", "candidates", len(candidates), "relevant", len(relevantItems), "duration_ms", relevanceMs)
	emit("screen", "done", fmt.Sprintf("%d 条", len(relevantItems)), relevanceMs)
	if len(relevantItems) == 0 {
		return roles, nil
	}

	emit("rerank", "start", fmt.Sprintf("%d 条", len(relevantItems)), 0)
	classifyStart := time.Now()
	classified, err := s.judgeClassifyBatches(ctx, qc, relevantItems)
	classifyMs := time.Since(classifyStart).Milliseconds()
	if err != nil {
		slog.Info("retrieval: rerank two-step classify timing", "candidates", len(relevantItems), "duration_ms", classifyMs, "error", err)
		emit("rerank", "error", err.Error(), classifyMs)
		return nil, fmt.Errorf("retrieval: rerank two-step classify: %w", err)
	}
	slog.Info("retrieval: rerank two-step classify timing", "candidates", len(relevantItems), "duration_ms", classifyMs)
	for cid, role := range classified {
		roles[cid] = role
	}
	return roles, nil
}

func (s *Service) judgeRelevanceBatches(ctx context.Context, qc QueryContext, candidates []rerankJudgeCandidate) (map[string]bool, error) {
	subject, intent, audience, constraint := judgeContextDefaults(qc)
	raw, err := s.runRerankJudgeBatches(ctx, candidates, "relevant", func(ctx context.Context, batch []rerankJudgeCandidate) (map[string]string, error) {
		relevantMap, err := s.judgeRelevanceExtractedEvidence(ctx, qc, subject, intent, audience, constraint, batch)
		if err != nil {
			return nil, err
		}
		out := make(map[string]string, len(relevantMap))
		for cid, ok := range relevantMap {
			if ok {
				out[cid] = "relevant"
			} else {
				out[cid] = "irrelevant"
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	relevant := make(map[string]bool, len(raw))
	for cid, v := range raw {
		relevant[cid] = v == "relevant"
	}
	return relevant, nil
}

func (s *Service) judgeClassifyBatches(ctx context.Context, qc QueryContext, candidates []rerankJudgeCandidate) (map[string]string, error) {
	subject, intent, audience, constraint := judgeContextDefaults(qc)
	return s.runRerankJudgeBatches(ctx, candidates, "supporting", func(ctx context.Context, batch []rerankJudgeCandidate) (map[string]string, error) {
		return s.judgeClassifyExtractedEvidence(ctx, qc, subject, intent, audience, constraint, batch)
	})
}

// judgeContextDefaults substitutes display placeholders for empty
// four-tuple fields — shared by every rerank judge prompt variant (combined
// and two-step split) so all three prompts see the same "（未提取）"/"（无）"
// convention.
func judgeContextDefaults(qc QueryContext) (subject, intent, audience, constraint string) {
	subject = qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent = qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}
	audience = qc.Audience
	if audience == "" {
		audience = "（未提取）"
	}
	constraint = qc.Constraint
	if constraint == "" {
		constraint = "（无）"
	}
	return subject, intent, audience, constraint
}

// runRerankJudgeBatches shares the batch-splitting + bounded-concurrency
// rerankJudgeCoverageRetries bounds how many times a single batch is
// re-sent to the LLM when its response silently omits a candidate_id that
// was in the input — observed in production: rerank_relevance.md /
// rerank_classify.md occasionally (even at temperature 0) drop one
// candidate_id from their "results" array entirely, and unlike an unknown
// candidate_id (which already errors below), a *missing* one was previously
// invisible — it just vanished from the merged map with no error, no log,
// no retry, silently shrinking the evidence set (e.g. losing a national-DB
// vendor's evidence out of an otherwise-stable candidate pool). This
// mirrors the same completeness check internal/evidence/service.go's
// validateCoverage already does for evidence_mine.md.
const rerankJudgeCoverageRetries = 2

// missingCandidateIDs returns the candidate_ids present in batch but absent
// from results.
func missingCandidateIDs(batch []rerankJudgeCandidate, results map[string]string) []string {
	var missing []string
	for _, c := range batch {
		if _, ok := results[c.CandidateID]; !ok {
			missing = append(missing, c.CandidateID)
		}
	}
	return missing
}

// fan-out used by both rerank_relevance.md and rerank_classify.md.
// callBatch judges one batch and returns per-candidate string values (role,
// or "relevant"/"irrelevant" for the relevance step); callers interpret
// them. defaultForMissing is the value assigned to any candidate_id still
// missing after rerankJudgeCoverageRetries retries — callers pick the safe
// side for their step (e.g. "relevant" for relevance, "supporting" for
// classify) so a dropped candidate degrades to "kept but unclassified"
// rather than silently disappearing.
func (s *Service) runRerankJudgeBatches(ctx context.Context, candidates []rerankJudgeCandidate, defaultForMissing string, callBatch func(ctx context.Context, batch []rerankJudgeCandidate) (map[string]string, error)) (map[string]string, error) {
	batches := splitRerankJudgeBatches(candidates, s.rerankJudgeBatchMaxChars())
	if len(batches) == 0 {
		return nil, nil
	}
	batchSizes := make([]int, len(batches))
	for i, b := range batches {
		batchSizes[i] = len(b)
	}
	slog.Info("retrieval: rerank judge batches", "batch_count", len(batches), "batch_sizes", batchSizes, "concurrency", s.rerankJudgeConcurrency())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	concurrency := s.rerankJudgeConcurrency()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make(map[string]string)
	var firstErr error
	var errOnce sync.Once

launchBatches:
	for i, batch := range batches {
		i, batch := i, batch
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launchBatches
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			batchStart := time.Now()
			var batchResults map[string]string
			var err error
			var missing []string
			for attempt := 0; attempt < rerankJudgeCoverageRetries; attempt++ {
				batchResults, err = callBatch(ctx, batch)
				if err != nil {
					break
				}
				missing = missingCandidateIDs(batch, batchResults)
				if len(missing) == 0 {
					break
				}
				slog.Warn("retrieval: rerank judge batch missing candidate_id(s) in response, retrying",
					"batch_index", i, "attempt", attempt, "missing", missing)
			}
			batchMs := time.Since(batchStart).Milliseconds()
			if err != nil {
				slog.Info("retrieval: rerank judge batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs, "error", err)
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			if len(missing) > 0 {
				slog.Warn("retrieval: rerank judge batch still missing candidate_id(s) after retries, defaulting",
					"batch_index", i, "missing", missing, "default", defaultForMissing)
				for _, cid := range missing {
					batchResults[cid] = defaultForMissing
				}
			}
			slog.Info("retrieval: rerank judge batch timing", "batch_index", i, "batch_size", len(batch), "duration_ms", batchMs)
			mu.Lock()
			for cid, v := range batchResults {
				results[cid] = v
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
	return results, nil
}

func (s *Service) judgeRelevanceExtractedEvidence(ctx context.Context, qc QueryContext, subject, intent, audience, constraint string, candidates []rerankJudgeCandidate) (map[string]bool, error) {
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank relevance payload: %w", err)
	}
	slog.Debug("retrieval: rerank relevance payload", "payload", string(payload))
	promptFile := "rerank_relevance.md"
	if s.cfg != nil && s.cfg.Retrieval.RerankRelevanceConcise {
		promptFile = "rerank_relevance_concise.md"
	}
	resp, err := s.llmClient.CompleteJSON(ctx, promptFile, map[string]string{
		"question":   qc.Question,
		"subject":    subject,
		"intent":     intent,
		"audience":   audience,
		"constraint": constraint,
		"candidates": string(payload),
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank relevance llm: %w", err)
	}

	var result rerankRelevanceResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("retrieval: rerank relevance parse: %w", err)
	}

	cidSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		cidSet[c.CandidateID] = true
	}
	relevant := make(map[string]bool, len(result.Results))
	for _, r := range result.Results {
		if !cidSet[r.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank relevance returned unknown candidate_id: %s", r.CandidateID)
		}
		slog.Info("retrieval: rerank relevance analysis", "question", qc.Question, "candidate_id", r.CandidateID, "relevant", r.Relevant, "analysis", r.Analysis)
		relevant[r.CandidateID] = r.Relevant
	}
	return relevant, nil
}

func (s *Service) judgeClassifyExtractedEvidence(ctx context.Context, qc QueryContext, subject, intent, audience, constraint string, candidates []rerankJudgeCandidate) (map[string]string, error) {
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank classify payload: %w", err)
	}
	slog.Debug("retrieval: rerank classify payload", "payload", string(payload))
	resp, err := s.llmClient.CompleteJSON(ctx, "rerank_classify.md", map[string]string{
		"question":   qc.Question,
		"subject":    subject,
		"intent":     intent,
		"audience":   audience,
		"constraint": constraint,
		"candidates": string(payload),
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank classify llm: %w", err)
	}

	var result rerankClassifyResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("retrieval: rerank classify parse: %w", err)
	}

	cidSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		cidSet[c.CandidateID] = true
	}
	roles := make(map[string]string, len(result.Results))
	for _, r := range result.Results {
		if !cidSet[r.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank classify returned unknown candidate_id: %s", r.CandidateID)
		}
		if r.Role != "direct" && r.Role != "supporting" {
			return nil, fmt.Errorf("retrieval: rerank classify invalid role: %s", r.Role)
		}
		slog.Info("retrieval: rerank classify analysis", "question", qc.Question, "candidate_id", r.CandidateID, "role", r.Role, "analysis", r.Analysis)
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

// splitRerankJudgeBatches packs candidates into batches capped at maxChars
// each, then balances candidates across those batches (LPT: largest items
// first, each placed into the currently-smallest batch that still fits) so
// that concurrent batches finish around the same time — the two-step judge's
// wall-clock cost is set by the slowest batch in a round, so an uneven
// packing (one near-full batch, one near-empty) wastes the concurrency
// budget. Candidate order carries no judging semantics (each candidate is
// judged independently), so reordering across batches is safe.
func splitRerankJudgeBatches(candidates []rerankJudgeCandidate, maxChars int) [][]rerankJudgeCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankJudgeBatchMaxChars
	}

	type sizedCandidate struct {
		candidate rerankJudgeCandidate
		chars     int
	}
	sized := make([]sizedCandidate, len(candidates))
	totalChars := 0
	for i, c := range candidates {
		itemJSON, _ := json.Marshal(c)
		n := utf8.RuneCount(itemJSON)
		sized[i] = sizedCandidate{candidate: c, chars: n}
		totalChars += n
	}
	sort.SliceStable(sized, func(i, j int) bool { return sized[i].chars > sized[j].chars })

	numBatches := (totalChars + maxChars - 1) / maxChars
	if numBatches < 1 {
		numBatches = 1
	}
	batches := make([][]rerankJudgeCandidate, numBatches)
	batchChars := make([]int, numBatches)

	for _, item := range sized {
		best := -1
		for b := 0; b < len(batches); b++ {
			if len(batches[b]) > 0 && batchChars[b]+item.chars > maxChars {
				continue
			}
			if best == -1 || batchChars[b] < batchChars[best] {
				best = b
			}
		}
		if best == -1 {
			batches = append(batches, nil)
			batchChars = append(batchChars, 0)
			best = len(batches) - 1
		}
		batches[best] = append(batches[best], item.candidate)
		batchChars[best] += item.chars
	}

	nonEmpty := make([][]rerankJudgeCandidate, 0, len(batches))
	for _, b := range batches {
		if len(b) > 0 {
			nonEmpty = append(nonEmpty, b)
		}
	}
	return nonEmpty
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

// rerankRelevanceResult / rerankClassifyResult are the two-step judge's
// per-prompt result shapes — see judgeRerankTwoStep.
type rerankRelevanceResult struct {
	Results []struct {
		CandidateID string `json:"candidate_id"`
		Relevant    bool   `json:"relevant"`
		Analysis    string `json:"analysis"`
	} `json:"results"`
}

type rerankClassifyResult struct {
	Results []struct {
		CandidateID string `json:"candidate_id"`
		Role        string `json:"role"`
		Analysis    string `json:"analysis"`
	} `json:"results"`
}

// Step 8: KPN expansion
// Returns updated candidates (with supporting neighbors) and conflict candidates separately.
//
// Seeds from the specific point judged direct (candidate.pointID), not every
// point in that point's KU (2026-08-08 决策: 收窄到 KP 级 — 原 MVP 步骤 8 是
// "对 direct KU 查邻居 KP"，即用该 KU 下全部知识点做种子；但一个 KU 常有
// 多条互不相关的 KP，每条各自的 KPN 边一起展开会把候选池撑得远大于这条
// direct 证据实际需要的邻居，直接推高 rerank 阶段要判断的候选量和 LLM
// 调用延迟。收窄后邻居仅来自这一条被判 direct 的 KP 自身的 KPN 关系。
func (s *Service) kpnExpand(candidates []candidate) ([]candidate, []candidate, error) {
	seedSet := make(map[string]bool)
	var seedPointIDs []string
	for _, c := range candidates {
		if len(c.sourcePaths) > 0 && c.sourcePaths[0] == "direct" && c.pointID != "" && !seedSet[c.pointID] {
			seedSet[c.pointID] = true
			seedPointIDs = append(seedPointIDs, c.pointID)
		}
	}
	if len(seedPointIDs) == 0 {
		return candidates, nil, nil
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

// judgeKPNExpansion re-checks the neighbors kpnExpand added (origin ==
// OriginKPNExpansion) against the LLM rerank judge before trusting them as
// supporting evidence. A KPN "related" edge only records that two points
// are topically linked (e.g. shared entities/wording) — it says nothing
// about whether the neighbor fits this question's object/scenario, so
// unlike Step 7's recalled candidates, expansion neighbors reach this point
// having never cleared any relevance check. Candidates that didn't come
// from kpnExpand pass through untouched. Any role the judge returns for an
// expansion neighbor is coerced to "supporting" — expansion never promotes
// a neighbor to direct, it only adds necessary context alongside it.
func (s *Service) judgeKPNExpansion(ctx context.Context, qc QueryContext, candidates []candidate) ([]candidate, []Evidence, error) {
	var base, expanded []candidate
	for _, c := range candidates {
		if c.origin == OriginKPNExpansion {
			expanded = append(expanded, c)
		} else {
			base = append(base, c)
		}
	}
	if len(expanded) == 0 {
		return candidates, nil, nil
	}

	// Only relevance needs checking here — a KPN neighbor's role is always
	// coerced to "supporting" below regardless of what a classify call would
	// say, so running one is a wasted round trip.
	kept, filtered, err := s.judgeCandidatesRelevanceOnly(ctx, qc, expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieval: judge kpn expansion: %w", err)
	}
	for i := range kept {
		kept[i].sourcePaths = []string{"supporting"}
	}
	return append(base, kept...), filtered, nil
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
	unitRecallInfo := make(map[string]candidate, len(direct)+len(supporting))
	for _, c := range append(append([]candidate{}, direct...), supporting...) {
		unitSourceIDs[c.unitID] = c.sourceID
		unitRecallInfo[c.unitID] = c
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
		recall := unitRecallInfo[item.UnitID]
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
			RecallPaths:  recall.recallOrigins,
			MergedRank:   recall.mergedRank,
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
