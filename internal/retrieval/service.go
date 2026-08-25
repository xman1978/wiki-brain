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
	subjectNormalizer  *SubjectNormalizer
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
	defaultRerankJudgeBatchMaxChars      = 2000
	defaultRerankJudgeBatchMaxCandidates = 5
	defaultRerankJudgeConcurrency        = 4
)

func NewService(store *Store, llmClient llm.LLMClient, unitsIdx, pointsIdx, outlinesIdx bleve.Index, cfg *config.Config, activationSvc *activation.Service, evidenceSvc *evidence.Service, wikiSvc *wiki.Service) *Service {
	subjectNormalizer := NewSubjectNormalizer(store, SubjectNormConfig{
		LocalSimMin: cfg.Retrieval.SourceAffinityLocalSimMin,
	})
	subjectNormalizer.SetLLMClient(llmClient)
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
		subjectNormalizer:  subjectNormalizer,
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

// RetrieveWithProgress dispatches to the Wiki 直答 and activation fast path
// (docs/impl/v1/retrieval.md 第 0/1 层) unless ForceFull is set or fast_path
// is disabled, falling back to the full MVP pipeline whenever neither path
// produces a usable result. 2026-08-19 改判：Wiki 直答的 Concept/Fact 识别要
// 调 LLM，比纯程序的 ActivationLink Match 慢得多；串行等 Wiki 先出结果拖慢
// 了本该走激活层就能秒回的问题。两层改为并行发起，都返回后按优先级挑选：
// ActivationLink 命中优先于 Wiki 直答（同时命中时激活层更快、更可信，Wiki
// 直答的额外 LLM 调用视为为了不阻塞激活层而接受的浪费成本）。When
// fast_path=false the match is still performed and its activation_hits are
// merged into the slow-path result — "记录命中日志后仍走慢路径" — so gated
// rollout can compare hit quality without changing behavior.
func (s *Service) RetrieveWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	if !qc.ForceFull {
		var wikiES *EvidenceSet
		var wikiOK bool
		var fastES *EvidenceSet
		var hits []ActivationHit
		var fastOK bool

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			wikiES, wikiOK = s.tryWikiAnswer(ctx, qc)
		}()
		go func() {
			defer wg.Done()
			fastES, hits, fastOK = s.tryFastPath(ctx, qc)
		}()
		wg.Wait()

		if fastOK {
			return fastES, nil
		}
		if wikiOK {
			return wikiES, nil
		}
		if len(hits) > 0 {
			slowES, err := s.retrieveSlowPath(ctx, qc, progress)
			if err != nil {
				return nil, err
			}
			slowES.ActivationHits = hits
			return slowES, nil
		}
		return s.retrieveSlowPath(ctx, qc, progress)
	}
	return s.retrieveSlowPath(ctx, qc, progress)
}

// tryWikiAnswer implements docs/impl/v1/retrieval.md 第 0 层: query the Wiki
// index before the activation fast path (不调 LLM 除非命中分达标), and if the
// hit page can sufficiently answer the question, return a path_type=wiki
// EvidenceSet without ever reaching the fast/slow path. wikiSvc is nil until
// main.go wires it up, matching the doc's "未实现时第 0 层跳过".
func (s *Service) tryWikiAnswer(ctx context.Context, qc QueryContext) (*EvidenceSet, bool) {
	if s.wikiSvc == nil {
		return nil, false
	}
	result, ok, err := s.wikiSvc.TryDirectAnswer(ctx, qc.Question, qc.DomainIDs, s.cfg.Retrieval.WikiMinScore, s.cfg.Retrieval.WikiMaxCandidates)
	if err != nil {
		slog.Warn("wiki direct-answer failed, falling back", "error", err)
		return nil, false
	}
	if !ok {
		return nil, false
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
		DirectEvidence:    s.buildWikiEvidence(result.CitedPointIDs),
		Supporting:        []Evidence{},
		WikiPageID:        result.PageID,
		CitedPointIDs:     result.CitedPointIDs,
		WikiAnswerContent: result.Content,
	}, true
}

// buildWikiEvidence resolves a Wiki direct answer's cited point_ids into
// displayable Evidence (docs/impl/v1/wiki.md 检索接入) so the "证据X" links
// embedded in a Wiki answer's content — which cite the compiled page's own
// point_ids verbatim (config/prompts/answer_wiki.md), not a freshly-minted
// Evidence.FactID like the mined pipeline does — resolve to something the
// evidence drawer can actually show. FactID is deliberately set to the
// point_id itself (not a random uuid.New()) so it lines up with what's
// literally embedded as `[point_id]` in the answer content; best-effort per
// point — a point whose KU/source lookup fails is silently dropped rather
// than failing the whole answer, since the answer text and citations list
// are already finalized by this point.
func (s *Service) buildWikiEvidence(pointIDs []string) []Evidence {
	if len(pointIDs) == 0 {
		return []Evidence{}
	}
	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: wiki evidence unit lookup failed", "error", err)
		return []Evidence{}
	}
	contents, err := s.store.GetPointContentsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: wiki evidence content lookup failed", "error", err)
		contents = map[string]string{}
	}

	out := make([]Evidence, 0, len(hits))
	for _, h := range hits {
		ref := SourceRef{SourceID: h.SourceID, LineStart: h.LineStart, LineEnd: h.LineEnd}
		refJSON, _ := json.Marshal(ref)
		out = append(out, Evidence{
			FactID:    h.PointID,
			UnitID:    h.UnitID,
			PointID:   h.PointID,
			Content:   contents[h.PointID],
			SourceRef: refJSON,
			Role:      evidence.RoleDirect,
			Origin:    OriginWiki,
			Mined:     false,
		})
	}
	return out
}

// RetrieveSlowPathWithProgress forces the full MVP pipeline, used by Answer's
// step-6b fallback to redo retrieval after a fast-path answer fails
// (docs/impl/v1/retrieval.md 步骤 6).
func (s *Service) RetrieveSlowPathWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	return s.retrieveSlowPath(ctx, qc, progress)
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
		normSubject, normIntent, normAudience, normConstraint, _, _, err := s.activationSvc.NormalizeTuple(ctx, qc.DomainIDs, qc.Subject, qc.Intent, qc.Audience, qc.Constraint)
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
	// 2026-08-20 重设计：Bundle Match 不再等 Link 出现跨 unit 歧义才被
	// consult，而是跟 Link Match 并行主动跑（docs/impl/v1/retrieval.md
	// 步骤 2「命中优先级」，同 RetrieveWithProgress 里 Wiki∥ActivationLink
	// 已有的并行写法）；两边都返回后按连续置信度（tier/mean）择一，不是写死
	// 的层级顺序。
	var matchDomainIDs []string
	if workQC.DomainResolved {
		matchDomainIDs = workQC.DomainIDs
	}
	var linkMatches []activation.LinkMatch
	var linkErr error
	var bundleCand bundleCandidate
	var bundleOK bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		linkMatches, linkErr = s.activationSvc.Match(ctx, expandedQuery, matchDomainIDs, matchCfg)
	}()
	go func() {
		defer wg.Done()
		bundleCand, bundleOK = s.resolveBundleCandidate(ctx, expandedQuery, matchDomainIDs, matchCfg)
	}()
	wg.Wait()

	if linkErr != nil {
		slog.Warn("retrieval: activation match failed", "error", linkErr)
		linkMatches = nil
	}
	linkMatches = s.filterMatchesByDomain(linkMatches, workQC)

	var activationHits []ActivationHit
	var verified []activation.LinkMatch
	linkClassified := false
	if len(linkMatches) > 0 {
		activationHits, verified, linkClassified = classifyActivationMatches(linkMatches)
	}

	var linkIDs, pointIDs []string
	var linkHits []DirectHit
	linkResolved := false
	if linkClassified {
		linkIDs, pointIDs = verifiedIDs(verified)
		var unitStatus unitResolutionStatus
		linkHits, unitStatus = s.resolveUnitsForPoints(pointIDs, linkIDs)
		linkResolved = unitStatus == unitResolutionOK
	}

	var hits []DirectHit
	var usedLinkIDs []string
	var usedBundleIDs []string
	var usedBundleHits []BundleHit
	switch {
	case linkResolved && bundleOK:
		linkTier, linkMean := bestLinkTierMean(verified)
		if tierRank(bundleCand.tier) > tierRank(linkTier) || (tierRank(bundleCand.tier) == tierRank(linkTier) && bundleCand.mean > linkMean) {
			hits = bundleCand.hits
			usedBundleIDs = bundleCand.bundleIDs
			usedBundleHits = bundleCand.hitInfo
		} else {
			hits = linkHits
			usedLinkIDs = linkIDs
		}
	case linkResolved:
		hits = linkHits
		usedLinkIDs = linkIDs
	case bundleOK:
		hits = bundleCand.hits
		usedBundleIDs = bundleCand.bundleIDs
		usedBundleHits = bundleCand.hitInfo
	default:
		return nil, activationHits, false
	}

	// Async, non-blocking — touch whichever side actually resolved this hit.
	go func() {
		if len(usedLinkIDs) > 0 {
			if err := s.activationSvc.TouchLastUsed(usedLinkIDs); err != nil {
				slog.Warn("retrieval: touch last used failed", "error", err)
			}
		}
		for _, bundleID := range usedBundleIDs {
			if err := s.activationSvc.Store().TouchBundleLastUsed(bundleID); err != nil {
				slog.Warn("retrieval: touch bundle last used failed", "bundle_id", bundleID, "error", err)
			}
		}
	}()

	es, resultHits, ok := s.finishFastPath(ctx, workQC, hits, usedLinkIDs, activationHits, usedBundleHits)
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
func (s *Service) finishFastPath(ctx context.Context, workQC QueryContext, hits []DirectHit, linkIDs []string, activationHits []ActivationHit, bundleHits []BundleHit) (*EvidenceSet, []ActivationHit, bool) {
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

	// 快路径不做 KPN 邻居扩展（kpnExpand）：ActivationLink/Bundle 关联的
	// point_id 本身就是历史验证过的、与问题相关的 KP，机制上已经保证了相关
	// 性——如果某个 KPN 邻居真的和问题相关，它应当自己也命中/沉淀出一条
	// ActivationLink，而不是靠快路径临时按图关系扩展进来。也因此不再需要
	// judgeKPNExpansion（KPN 相关性判定，rerank_relevance.md）这道过滤。
	// buildEvidenceSet 内的证据挖掘（evidence_mine.md）也一并跳过，证据内容
	// 退化为整段落（mined=false），是"挖掘失败时的整段兜底"已有语义路径。
	es, err := s.buildEvidenceSet(ctx, workQC.Question, workQC.Subject, workQC.Intent, workQC.Audience, workQC.Constraint, "short", direct, nil, nil, nil, false, true)
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
		sufficient, needsDeep, _, _, err := s.VerifyEvidenceSufficient(ctx, workQC.Question, es)
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
	es.BundleHits = bundleHits
	slog.Info("retrieval: fast path evidence built",
		"direct", len(es.DirectEvidence), "supporting", len(es.Supporting), "link_ids", linkIDs)
	return es, activationHits, true
}

// SlowPathVerifyEnabled reports whether Answer should run the slow-path
// sufficiency gate (docs/impl/v1/retrieval.md 步骤 2b) before generating.
func (s *Service) SlowPathVerifyEnabled() bool {
	return s.cfg != nil && s.cfg.Retrieval.SlowPathVerify
}

// PoolWidenEnabled reports whether Answer should attempt the candidate-pool
// widen retry (N→2N, docs/design/topn-coefficient-convergence.md 第 3 节)
// when content-widening still leaves evidence insufficient. Defaults off —
// this adds an extra rerank+verify round-trip on an already-insufficient
// trace, so it's opt-in during the data-collection phase.
func (s *Service) PoolWidenEnabled() bool {
	return s.cfg != nil && s.cfg.Retrieval.PoolWidenEnabled
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
func (s *Service) VerifyEvidenceSufficient(ctx context.Context, question string, es *EvidenceSet) (sufficient bool, needsDeep bool, reason string, contentWidened bool, err error) {
	sufficient, needsDeep, reason, err = s.verifyEvidenceSufficientOnce(ctx, question, es)
	if err != nil || sufficient {
		return sufficient, needsDeep, reason, false, err
	}

	// Fallback: a sufficient=false verdict may be an artifact of evidence
	// mining under-extracting a candidate (see internal/evidence's mining
	// step) rather than the source material genuinely lacking the answer —
	// mining picks verbatim fragments per candidate and can drop content a
	// full read of the same knowledge unit would have kept. Before trusting
	// the "insufficient" verdict, re-verify once against each evidence
	// item's full, unmined knowledge-unit text, so a mining gap doesn't
	// masquerade as a genuine source-material gap. Only evidence items whose
	// UnitID resolves are widened; anything unresolvable keeps its original
	// (mined) content and still contributes to the fallback attempt.
	widened, changed := s.widenEvidenceToFullUnits(es)
	if !changed {
		return sufficient, needsDeep, reason, false, nil
	}
	slog.Info("retrieval: evidence verify insufficient, retrying with full KU content", "reason", reason)
	fbSufficient, fbNeedsDeep, fbReason, fbErr := s.verifyEvidenceSufficientOnce(ctx, question, widened)
	if fbErr != nil {
		slog.Warn("retrieval: full-KU evidence verify retry failed, keeping original verdict", "error", fbErr)
		return sufficient, needsDeep, reason, false, nil
	}
	if fbSufficient {
		slog.Info("retrieval: full-KU evidence verify retry judged sufficient, mining had dropped needed content", "reason", fbReason)
		*es = *widened
		return fbSufficient, fbNeedsDeep, fbReason, true, nil
	}
	return sufficient, needsDeep, reason, false, nil
}

// WidenAndRetry re-runs Step 7-10 against a wider slice of the pre-truncation
// RRF-merged candidate pool ([0, 2N) instead of [0, N)) — for use when the
// sufficiency judge (VerifyEvidenceSufficient, including its content-widen
// retry) still finds the original top-N insufficient
// (docs/design/topn-coefficient-convergence.md 第 3 节). ok=false means there
// was no wider pool to try (candidatePool missing, or it never had more than
// N candidates to begin with) — the caller should keep the original
// insufficient verdict and not treat this as an error.
func (s *Service) WidenAndRetry(ctx context.Context, es *EvidenceSet) (widened *EvidenceSet, ok bool, err error) {
	if es.TopNAtBuild <= 0 || len(es.candidatePool) <= es.TopNAtBuild {
		return nil, false, nil
	}
	widerN := es.TopNAtBuild * 2
	pool := es.candidatePool
	if len(pool) > widerN {
		pool = pool[:widerN]
	} else {
		widerN = len(pool)
	}

	expanded, err := s.expandCandidatesToPoints(pool)
	if err != nil {
		return nil, false, fmt.Errorf("retrieval: widen and retry: expand candidates to points: %w", err)
	}

	qc := QueryContext{Question: es.Question, Subject: es.Subject, Intent: es.Intent, Audience: es.Audience, Constraint: es.Constraint}
	noopEmit := func(string, string, string, int64) {}
	newEs, err := s.rerankAndBuildEvidenceSet(ctx, qc, expanded, noopEmit, nil, false)
	if err != nil {
		return nil, false, fmt.Errorf("retrieval: widen and retry: rerank: %w", err)
	}
	newEs.candidatePool = es.candidatePool
	newEs.CandidatePoolSize = es.CandidatePoolSize
	newEs.TopNAtBuild = es.TopNAtBuild
	newEs.CoefficientAtBuild = es.CoefficientAtBuild
	newEs.WidenedToN = widerN
	return newEs, true, nil
}

// verifyEvidenceSufficientOnce is the single fast_verify.md call — factored
// out so VerifyEvidenceSufficient can run it twice (mined evidence, then
// full-KU evidence) without duplicating the prompt-call plumbing.
func (s *Service) verifyEvidenceSufficientOnce(ctx context.Context, question string, es *EvidenceSet) (sufficient bool, needsDeep bool, reason string, err error) {
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

// widenEvidenceToFullUnits returns a copy of es with each DirectEvidence/
// Supporting item's Content replaced by its owning knowledge unit's full raw
// text (falling back to the original mined content if the unit or source
// text can't be read). changed reports whether at least one item was
// actually widened, so the caller can skip a pointless retry when nothing
// could be expanded (e.g. all lookups failed).
func (s *Service) widenEvidenceToFullUnits(es *EvidenceSet) (*EvidenceSet, bool) {
	widened := *es
	widened.DirectEvidence = append([]Evidence(nil), es.DirectEvidence...)
	widened.Supporting = append([]Evidence(nil), es.Supporting...)

	changed := false
	fullContent := make(map[string]string) // unit_id -> full content, cached across direct+supporting
	widen := func(items []Evidence) {
		for i, ev := range items {
			if ev.UnitID == "" {
				continue
			}
			content, ok := fullContent[ev.UnitID]
			if !ok {
				unit, err := s.store.GetUnitByID(ev.UnitID)
				if err != nil {
					slog.Debug("retrieval: widen evidence to full unit, unit lookup failed", "unit_id", ev.UnitID, "error", err)
					fullContent[ev.UnitID] = ""
					continue
				}
				text, err := s.readUnitContent(unit.SourceID, unit.LineStart, unit.LineEnd)
				if err != nil {
					slog.Debug("retrieval: widen evidence to full unit, content read failed", "unit_id", ev.UnitID, "error", err)
					fullContent[ev.UnitID] = ""
					continue
				}
				content = text
				fullContent[ev.UnitID] = content
			}
			if content == "" || content == items[i].Content {
				continue
			}
			items[i].Content = content
			changed = true
		}
	}
	widen(widened.DirectEvidence)
	widen(widened.Supporting)
	return &widened, changed
}

func (s *Service) retrieveSlowPath(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	emit := func(phase, status, detail string, dur int64) {
		if progress != nil {
			progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
		}
	}

	if es, ok := s.trySourceAffinityShortcut(ctx, qc, emit, progress); ok {
		return es, nil
	}

	// Step 2: Domain pre-filter
	domainStart := time.Now()
	candidateSources, err := s.domainPreFilter(ctx, qc)
	if err != nil {
		emit("activation", "error", err.Error(), time.Since(domainStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: domain pre-filter: %w", err)
	}
	slog.Info("retrieval: step2 domain pre-filter done", "candidates", len(candidateSources))

	es, err := s.filterAndRecall(ctx, qc, candidateSources, emit, progress, false)
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

			// Subject affinity shortcut, second attempt (2026-08-25): the
			// original qc entering this function had no subject to look a
			// binding up under (bare POST /answer, no upstream session
			// parsing — trySourceAffinityShortcut's first attempt above
			// no-ops whenever qc.Subject == ""), so retry it now that this
			// fallback's own reparse has produced one.
			if es, ok := s.trySourceAffinityShortcut(ctx, retryQC, emit, progress); ok {
				return es, nil
			}

			es, err = s.filterAndRecall(ctx, retryQC, candidateSources, emit, progress, false)
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
	return s.recallFromSources(ctx, retryQC, candidateSources, emit, progress, true)
}

func evidenceEmpty(es *EvidenceSet) bool {
	return es == nil || (len(es.DirectEvidence) == 0 && len(es.Supporting) == 0)
}

// filterAndRecall runs Step 3 (Source semantic filter) then Steps 4-10 within
// the filtered source set.
func (s *Service) filterAndRecall(ctx context.Context, qc QueryContext, candidateSources []SourceInfo, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool) (*EvidenceSet, error) {
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

	return s.recallFromSources(ctx, qc, filteredSources, emit, progress, lastResort)
}

// recallFromSources runs Steps 4-10 (outline+FTS recall through
// EvidenceSet construction) against a fixed source set. lastResort is
// forwarded to buildEvidenceSet — see retrieveSlowPath's fallback 2 for what
// it changes.
func (s *Service) recallFromSources(ctx context.Context, qc QueryContext, sources []SourceInfo, emit func(phase, status, detail string, dur int64), progress ProgressFunc, lastResort bool) (*EvidenceSet, error) {
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

	// Step 6: RRF merge（outline + fts(question) + fts(四元组)）— returns the
	// FULL sorted pool (mergedRank assigned across all of it, not just the
	// eventual top-N); truncation happens below so the untruncated pool can
	// be kept for the pool-widen retry / calibration snapshot
	// (docs/design/topn-coefficient-convergence.md).
	fullPool := s.rrfMerge(outlineCandidates, ftsQuestion, ftsTuple)
	slog.Info("retrieval: step6 rrf merge done", "merged", len(fullPool))

	if len(fullPool) == 0 {
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

	topN := s.currentRerankTopN()
	truncated := fullPool
	if len(truncated) > topN {
		truncated = truncated[:topN]
	}

	// rrfMerge/ftsRecall pick each KU's single best-scoring KP as that KU's
	// stand-in candidate — a shortcut for ranking the KU, not a claim that
	// this is the only KP worth judging. Expanding here restores the KU's
	// full current KP set before judging, so a KU that wins its way into the
	// top-N by one KP's score doesn't silently hide its other KPs (which may
	// be the ones that actually answer the question) from the judge.
	expanded, err := s.expandCandidatesToPoints(truncated)
	if err != nil {
		return nil, fmt.Errorf("retrieval: expand candidates to points: %w", err)
	}
	slog.Info("retrieval: step6b expand candidates to points done", "units", len(truncated), "points", len(expanded))

	es, err := s.rerankAndBuildEvidenceSet(ctx, qc, expanded, emit, progress, lastResort)
	if err != nil {
		return nil, err
	}
	es.candidatePool = fullPool
	es.CandidatePoolSize = len(fullPool)
	es.TopNAtBuild = topN
	es.CoefficientAtBuild = s.cfg.Retrieval.OutlineRRFBoost
	return es, nil
}

// rerankAndBuildEvidenceSet runs Steps 7-10 (Rerank through EvidenceSet
// construction) given an already-assembled candidate set. Factored out of
// recallFromSources so other callers can reuse it without going through
// Domain/Source/Outline/FTS at all.
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
	es, err := s.buildEvidenceSet(ctx, question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, path, direct, supporting, conflictCandidates, progress, lastResort, false)
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

// sourceFilterCandidate is one source_filter.md candidate item — mirrors
// rerankJudgeCandidate's shape (a small JSON-friendly struct rather than a
// hand-built text list) so it packs through the same splitJudgeBatches sizing
// logic.
type sourceFilterCandidate struct {
	CandidateID string `json:"candidate_id"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
}

func sourceFilterCandidateID(c sourceFilterCandidate) string { return c.CandidateID }

// Step 3: Source semantic filter (2026-08-25 改为分批并发调用，复用
// judge_batch.go 的批量核心 — 见 docs 讨论：候选 source 数量随导入文件增长，
// 原先把全部候选一次性塞进一个 LLM 调用的 prompt 大小和延迟会跟着线性增长；
// 现在按字符数/条数上限拆批、有界并发调用，单次调用大小和总墙钟延迟都不再
// 随语料量无界增长). LLM 整体失败（或返回缺失过多以致某批次最终报错）时
// fail-open：保留全部候选，交给后续 outline/FTS 召回与证据判断兜底，语义与
// 分批前一致。
func (s *Service) sourceSemanticFilter(ctx context.Context, qc QueryContext, candidates []SourceInfo) ([]SourceInfo, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	items := make([]sourceFilterCandidate, len(candidates))
	bySourceID := make(map[string]SourceInfo, len(candidates))
	for i, src := range candidates {
		summary := ""
		if src.Summary.Valid {
			summary = src.Summary.String
		}
		items[i] = sourceFilterCandidate{CandidateID: src.SourceID, Title: src.Title, Summary: summary}
		bySourceID[src.SourceID] = src
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

	callBatch := func(ctx context.Context, batch []sourceFilterCandidate) (map[string]string, error) {
		payload, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("retrieval: source filter payload: %w", err)
		}
		resp, err := s.llmClient.CompleteJSON(ctx, "source_filter.md", map[string]string{
			"question":   qc.Question,
			"subject":    subject,
			"intent":     intent,
			"audience":   audience,
			"constraint": constraint,
			"candidates": string(payload),
		}, "classification")
		if err != nil {
			return nil, fmt.Errorf("retrieval: source filter llm: %w", err)
		}
		var result struct {
			Results []struct {
				CandidateID string `json:"candidate_id"`
				Relevant    bool   `json:"relevant"`
			} `json:"results"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("retrieval: source filter parse: %w", err)
		}
		out := make(map[string]string, len(result.Results))
		for _, r := range result.Results {
			if r.Relevant {
				out[r.CandidateID] = "relevant"
			} else {
				out[r.CandidateID] = "irrelevant"
			}
		}
		return out, nil
	}

	results, err := runJudgeBatches(ctx, items, s.rerankJudgeBatchMaxChars(), s.rerankJudgeBatchMaxCandidates(), s.rerankJudgeConcurrency(), sourceFilterCandidateID, "relevant", callBatch)
	if err != nil {
		slog.Warn("retrieval: source filter failed, using all candidates", "error", err)
		return candidates, nil
	}

	var filtered []SourceInfo
	for _, src := range candidates {
		if results[src.SourceID] == "relevant" {
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

// outlineFilterCandidate is one outline_filter.md candidate item, mirroring
// sourceFilterCandidate's shape. Flattened across every low-FTS-score source
// in one candidate pool (2026-08-25) instead of one goroutine+LLM call per
// source — a domain with many low-scoring sources no longer means an
// unbounded number of concurrent LLM calls; call count is now bounded by
// splitJudgeBatches' char/count caps regardless of source count. OutlineID is
// globally unique (source_outlines primary key) so items from different
// sources can share one flat candidate pool without collision; Level carries
// the outline depth that the old per-source indented text list conveyed.
type outlineFilterCandidate struct {
	CandidateID string `json:"candidate_id"`
	Title       string `json:"title"`
	Level       int    `json:"level,omitempty"`
	Keywords    string `json:"keywords,omitempty"`
}

func outlineFilterCandidateID(c outlineFilterCandidate) string { return c.CandidateID }

func (s *Service) outlineLLMFallback(ctx context.Context, qc QueryContext, sourceIDs []string) ([]string, error) {
	outlines, err := s.store.GetOutlinesBySourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}
	if len(outlines) == 0 {
		return nil, nil
	}

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}

	items := make([]outlineFilterCandidate, len(outlines))
	for i, o := range outlines {
		keywords := ""
		if o.Summary.Valid {
			keywords = o.Summary.String
		}
		items[i] = outlineFilterCandidate{CandidateID: o.OutlineID, Title: o.Title, Level: o.Level, Keywords: keywords}
	}

	callBatch := func(ctx context.Context, batch []outlineFilterCandidate) (map[string]string, error) {
		payload, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("retrieval: outline filter payload: %w", err)
		}
		resp, err := s.llmClient.CompleteJSON(ctx, "outline_filter.md", map[string]string{
			"question":   qc.Question,
			"subject":    subject,
			"intent":     intent,
			"candidates": string(payload),
		}, "classification")
		if err != nil {
			return nil, fmt.Errorf("retrieval: outline filter llm: %w", err)
		}
		var result struct {
			Results []struct {
				CandidateID string `json:"candidate_id"`
				Relevant    bool   `json:"relevant"`
			} `json:"results"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("retrieval: outline filter parse: %w", err)
		}
		out := make(map[string]string, len(result.Results))
		for _, r := range result.Results {
			if r.Relevant {
				out[r.CandidateID] = "relevant"
			} else {
				out[r.CandidateID] = "irrelevant"
			}
		}
		return out, nil
	}

	// LLM 失败/最终报错时保持原有 fail-open 语义：outlineRecall 只把返回的 ID
	// 追加进 FTS 已召回的结果，一个 error 上抛后由调用方 slog.Warn 后继续（见
	// outlineRecall 的 4.2 步骤），不需要在这里额外 fallback。
	results, err := runJudgeBatches(ctx, items, s.rerankJudgeBatchMaxChars(), s.rerankJudgeBatchMaxCandidates(), s.rerankJudgeConcurrency(), outlineFilterCandidateID, "irrelevant", callBatch)
	if err != nil {
		return nil, err
	}

	var allIDs []string
	for _, o := range outlines {
		if results[o.OutlineID] == "relevant" {
			allIDs = append(allIDs, o.OutlineID)
		}
	}
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
// rrfMerge returns the FULL sorted candidate list (mergedRank assigned across
// all of it) — it no longer truncates to rerank_top_n itself; callers slice
// to the N they need. This lets recallFromSources keep the untruncated pool
// around for the pool-widen retry / calibration snapshot
// (docs/design/topn-coefficient-convergence.md).
func (s *Service) rrfMerge(lists ...[]candidate) []candidate {
	const k = RRFK

	type mergedCandidate struct {
		candidate
		rrfScore   float64
		paths      map[string]bool
		rankByPath map[string]int
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
				m.rankByPath[pathName] = rank
				if c.pointID != "" && m.pointID == "" {
					m.pointID = c.pointID
				}
			} else {
				merged[c.unitID] = &mergedCandidate{
					candidate:  c,
					rrfScore:   rrfScore,
					paths:      map[string]bool{pathName: true},
					rankByPath: map[string]int{pathName: rank},
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
		m.candidate.rankByPath = m.rankByPath
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

	return result
}

// currentRerankTopN reads the configured rerank_top_n, applying the same
// <=0 fallback rrfMerge used to enforce internally before truncation moved
// to recallFromSources.
func (s *Service) currentRerankTopN() int {
	topN := s.cfg.Retrieval.RerankTopN
	if topN <= 0 {
		topN = 20
	}
	return topN
}

// expandCandidatesToPoints turns each of rrfMerge's top-N KU-level
// candidates into one candidate per that KU's own current knowledge points,
// so every KP belonging to a selected KU gets its own judge candidate.
// Without this, only the single KP that ftsRecall/rrfMerge happened to rank
// the KU by (bestPointPerUnit) would ever reach buildJudgeItems — any other
// KP under the same KU, however relevant, would never be seen by the judge
// at all (not even filed as "irrelevant").
func (s *Service) expandCandidatesToPoints(candidates []candidate) ([]candidate, error) {
	unitIDs := make([]string, len(candidates))
	for i, c := range candidates {
		unitIDs[i] = c.unitID
	}
	pointsByUnit, err := s.store.GetPointContentsByUnitIDs(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: expand candidates to points: %w", err)
	}

	expanded := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		points := pointsByUnit[c.unitID]
		if len(points) == 0 {
			// No current KP found for this unit (e.g. a lifecycle change
			// raced the query moments after FTS recall) — keep the
			// original single-point candidate rather than dropping the
			// whole unit; filterCurrentUnits/buildJudgeItems downstream
			// will surface the inconsistency if it's real.
			expanded = append(expanded, c)
			continue
		}
		for _, p := range points {
			pc := c
			pc.pointID = p.PointID
			expanded = append(expanded, pc)
		}
	}
	return expanded, nil
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
	centersByUnit, err := s.store.GetUnitCenters(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank get centers: %w", err)
	}
	pointsByUnit, err := s.store.GetPointContentsByUnitIDs(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank get points: %w", err)
	}
	pointByID := make(map[string]PointFact, len(candidates))
	for _, facts := range pointsByUnit {
		for _, f := range facts {
			pointByID[f.PointID] = f
		}
	}

	// A point's persisted semantics are used regardless of which extraction
	// prompt version produced them — semantics_prompt_version is kept on the
	// row for diagnostics only, not as a completeness gate. Requiring an
	// exact match meant every prompt wording tweak instantly broke rerank
	// for the whole existing corpus until every source was re-extracted;
	// only a genuinely missing row (nothing to feed the judge with at all)
	// is a real integrity problem.
	missingSet := make(map[string]struct{})
	var staleCount int
	for _, c := range candidates {
		fact, ok := pointByID[c.pointID]
		if !ok {
			missingSet[c.pointID] = struct{}{}
			continue
		}
		if fact.SemanticsPromptVersion != rerank.ExtractPromptVersion {
			staleCount++
		}
	}
	if staleCount > 0 {
		slog.Debug("retrieval: rerank using semantics from an older/missing extraction prompt version", "stale_count", staleCount)
	}
	if len(missingSet) > 0 {
		return nil, fmt.Errorf("retrieval: rerank semantics integrity: missing point_ids: %s", strings.Join(sortedUnitIDs(missingSet), ", "))
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
			c.candidateID, titleCache[c.sourceID], centersByUnit[c.unitID], c.pointID, pointsByUnit[c.unitID]))
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
// candidate. center (knowledge_units.center, from unit extraction) still
// covers the whole KU, but points is scoped to just this candidate's own
// targetPointID — a candidate is always recalled against one specific KP
// (see the candidate struct's pointID), and previously this fed the judge
// every sibling KP in the same unit regardless of which one the candidate
// actually represents. A unit's KPs can have different objects/audiences
// (a policy document mixing a sales-commission clause with an unrelated
// eligibility clause, say), so bundling them let one sibling's wording
// launder relevance onto another — the judge would see the whole unit's
// text as one blob and let a loosely matching phrase in an unrelated KP
// carry a different KP's candidate across the object/scenario gate. If
// targetPointID isn't found in points (e.g. a lifecycle change raced the
// query), fall back to the full unit's points rather than judging with an
// empty payload.
func buildRerankJudgeCandidate(candidateID, sourceTitle, center, targetPointID string, points []PointFact) rerankJudgeCandidate {
	var target *PointFact
	for i := range points {
		if points[i].PointID == targetPointID {
			target = &points[i]
			break
		}
	}

	judgePoints := make([]rerankJudgePoint, 0, 1)
	if target != nil {
		judgePoints = append(judgePoints, rerankJudgePoint{Content: target.Content, Type: target.PointType})
	} else {
		judgePoints = make([]rerankJudgePoint, len(points))
		for i, p := range points {
			judgePoints[i] = rerankJudgePoint{Content: p.Content, Type: p.PointType}
		}
	}

	var semantic PointFact
	if target != nil {
		semantic = *target
	} else if len(points) > 0 {
		// targetPointID not found among this unit's current points (e.g. a
		// lifecycle change raced the query) — fall back to the first
		// sibling's semantics rather than judging with an empty payload.
		semantic = points[0]
	}
	return rerankJudgeCandidate{
		CandidateID:  candidateID,
		SourceTitle:  sourceTitle,
		Center:       center,
		SourceTheme:  semantic.SourceTheme,
		ContentTheme: semantic.ContentTheme,
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
// rerankJudgeCoverageRetries / missingCandidateIDs / runRerankJudgeBatches
// are now thin wrappers over judge_batch.go's generic
// judgeBatchCoverageRetries/missingJudgeIDs/runJudgeBatches — the packing +
// bounded-concurrency + missing-id-retry core is shared with
// sourceSemanticFilter and outlineLLMFallback (2026-08-25), so scaling
// behavior (bounded prompt size per call, bounded concurrent calls) doesn't
// have to be reimplemented per LLM classification step.
const rerankJudgeCoverageRetries = judgeBatchCoverageRetries

func rerankCandidateID(c rerankJudgeCandidate) string { return c.CandidateID }

// missingCandidateIDs returns the candidate_ids present in batch but absent
// from results.
func missingCandidateIDs(batch []rerankJudgeCandidate, results map[string]string) []string {
	return missingJudgeIDs(batch, results, rerankCandidateID)
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
	return runJudgeBatches(ctx, candidates, s.rerankJudgeBatchMaxChars(), s.rerankJudgeBatchMaxCandidates(), s.rerankJudgeConcurrency(), rerankCandidateID, defaultForMissing, callBatch)
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

func (s *Service) rerankJudgeBatchMaxCandidates() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankJudgeBatchMaxCandidates > 0 {
		return s.cfg.Retrieval.RerankJudgeBatchMaxCandidates
	}
	return defaultRerankJudgeBatchMaxCandidates
}

func (s *Service) rerankJudgeConcurrency() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankJudgeConcurrency > 0 {
		return s.cfg.Retrieval.RerankJudgeConcurrency
	}
	return defaultRerankJudgeConcurrency
}

// splitRerankJudgeBatches packs candidates into batches capped at maxChars
// each and at maxCandidates candidates each — whichever cap is stricter for
// a given batch — then balances candidates across those batches (LPT:
// largest items first, each placed into the currently-smallest batch that
// still fits) so that concurrent batches finish around the same time — the
// two-step judge's wall-clock cost is set by the slowest batch in a round,
// so an uneven packing (one near-full batch, one near-empty) wastes the
// concurrency budget. The candidate-count cap exists because the model's
// per-candidate output (relevant + a one-sentence analysis) can push a
// large batch past its output token budget and get truncated mid-JSON even
// while comfortably under maxChars, which only bounds the candidates' own
// input size, not the model's response size. Candidate order carries no
// judging semantics (each candidate is judged independently), so
// reordering across batches is safe.
func splitRerankJudgeBatches(candidates []rerankJudgeCandidate, maxChars, maxCandidates int) [][]rerankJudgeCandidate {
	return splitJudgeBatches(candidates, maxChars, maxCandidates)
}

type rerankJudgeCandidate struct {
	CandidateID  string             `json:"candidate_id"`
	SourceTitle  string             `json:"source_title"`
	Center       string             `json:"center"`
	SourceTheme  string             `json:"source_theme"`
	ContentTheme string             `json:"content_theme"`
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
func (s *Service) buildEvidenceSet(ctx context.Context, question, subject, intent, audience, constraint, path string, direct, supporting, conflicts []candidate, progress ProgressFunc, lastResort bool, skipMining bool) (*EvidenceSet, error) {
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

	// Each candidate's own KP content (the abstracted claim retrieval judged
	// relevant) is looked up so mining can be told what claim it's mining
	// verbatim support for, alongside the question — mining candidates only
	// carried the KU's raw text before, so the mining LLM had to guess
	// unaided how much of that raw text a given claim needs.
	pointsByUnit, err := s.store.GetPointContentsByUnitIDs(unitIDs)
	if err != nil {
		return nil, fmt.Errorf("retrieval: get evidence point contents: %w", err)
	}
	pointContent := make(map[string]string)
	semanticsByPoint := make(map[string]PointFact)
	for _, facts := range pointsByUnit {
		for _, f := range facts {
			pointContent[f.PointID] = f.Content
			semanticsByPoint[f.PointID] = f
		}
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
				Content: content, PointContent: pointContent[c.pointID], Role: role, Origin: origin,
			})
		}
	}
	appendItems(direct, evidence.RoleDirect)
	appendItems(supporting, evidence.RoleSupporting)

	emit("evidence", "start", fmt.Sprintf("%d 条候选", len(items)), 0)
	evidenceStart := time.Now()
	mined := items
	if !skipMining && s.evidenceSvc != nil {
		mined = s.evidenceSvc.Mine(ctx, question, subject, intent, audience, constraint, items, lastResort)
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
		semantic := semanticsByPoint[item.PointID]
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
