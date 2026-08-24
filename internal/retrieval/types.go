package retrieval

import "encoding/json"

type QueryContext struct {
	Question   string
	Subject    string
	Intent     string
	Audience   string
	Constraint string
	// DomainIDs from session merged parse+domain. When DomainResolved is true,
	// slow path skips question_domain_match and uses these IDs (empty ⇒ all
	// sources). Fast path filters ActivationLink candidates by source domain.
	DomainIDs      []string
	DomainResolved bool
	// FollowUp is true for non-first questions in a session (Session layer).
	// Fast-path tuple normalization against domain condition groups no longer
	// requires FollowUp — it runs before Match whenever DomainResolved and
	// domain verified condition groups are non-empty.
	FollowUp bool
	// ForceFull skips the activation fast path and forces the full MVP
	// pipeline — POST /retrieval's force_full request field
	// (docs/impl/v1/retrieval.md 步骤 7), for debugging/comparison evals.
	ForceFull bool
}

type EvidenceSet struct {
	Question       string             `json:"question"`
	Subject        string             `json:"subject,omitempty"`
	Intent         string             `json:"intent,omitempty"`
	Audience       string             `json:"audience,omitempty"`
	Constraint     string             `json:"constraint,omitempty"`
	Path           string             `json:"path"`
	PathType       string             `json:"path_type"`
	ActivationHits []ActivationHit    `json:"activation_hits"`
	BundleHits     []BundleHit        `json:"bundle_hits,omitempty"`
	DirectEvidence []Evidence         `json:"direct_evidence"`
	Supporting     []Evidence         `json:"supporting"`
	Conflicts      []ConflictEvidence `json:"conflicts,omitempty"`

	// GapReason and FilteredEvidence are slow-path-only diagnostics for
	// knowledge gaps (docs/impl/v1/retrieval.md 步骤 6, docs/impl/v1/study.md
	// "knowledge_gaps 表扩展"). GapReason is set to no_candidates when RRF
	// merge produces zero candidates, or judge_filtered when candidates
	// existed but rerank judged all of them irrelevant (direct+supporting
	// still empty after sufficiency check); left empty otherwise.
	// FilteredEvidence holds the candidates rerank judged "irrelevant" —
	// role=RoleIrrelevant, FactID left empty (not mined, not citable) —
	// kept regardless of GapReason so a non-gap answer can still show what
	// was considered and excluded.
	GapReason        string     `json:"gap_reason,omitempty"`
	FilteredEvidence []Evidence `json:"filtered_evidence,omitempty"`

	// Wiki direct-answer fields (docs/impl/v1/wiki.md 步骤 4). Only meaningful
	// when PathType==PathTypeWiki; this is the entire persisted
	// evidence_snapshot shape for that path ("{wiki_page_id, cited_point_ids}"
	// — no DirectEvidence/Supporting for wiki answers).
	WikiPageID    string   `json:"wiki_page_id,omitempty"`
	CitedPointIDs []string `json:"cited_point_ids,omitempty"`
	// WikiAnswerContent is the final answer text Retrieval's Wiki layer
	// already generated (docs/impl/v1/wiki.md 步骤 4's answer_wiki.md call) —
	// a side channel Answer consumes directly without its own LLM call.
	// Deliberately excluded from evidence_snapshot (json:"-"): the doc's
	// persisted shape has no content field, only wiki_page_id/cited_point_ids.
	WikiAnswerContent string `json:"-"`

	// —— top-N / 目录检索系数自收敛校准（docs/design/topn-coefficient-convergence.md）——
	// 只在 PathType==PathTypeFull 且慢路径充分性判断（checkSlowPathSufficiency）
	// 跑过之后才有意义；Fast/Wiki path 不涉及候选截断，这些字段留零值。
	//
	// CompletenessClass 是这条 trace 在证据充分性判断 + 候选池扩展重试链路上
	// 落入的五类结果之一，见下方常量。
	CompletenessClass string `json:"completeness_class,omitempty"`
	// CandidatePoolSize 是 Step 6 RRF merge 产出、任何截断之前的候选总数。
	CandidatePoolSize int `json:"candidate_pool_size,omitempty"`
	// TopNAtBuild/CoefficientAtBuild 是这次查询实际生效的 N 与目录检索系数
	// （即便后续触发了池扩展重试，这里仍记录扩展前的原始 N，扩展后的边界见
	// WidenedToN）。
	TopNAtBuild        int     `json:"top_n_at_build,omitempty"`
	CoefficientAtBuild float64 `json:"coefficient_at_build,omitempty"`
	// WidenedToN 非零时表示这条 trace 触发过候选池扩展重试，值是扩展后使用的
	// 候选数上限（通常是 2*TopNAtBuild）。
	WidenedToN int `json:"widened_to_n,omitempty"`

	// candidatePool 是 Step 6 RRF merge 排序后、任何截断之前的候选全集
	// （含 mergedRank/rankByPath），供 WidenAndRetry 与
	// CalibrationPoolSnapshot 使用。不导出到 JSON——只在同一次查询的生命周期
	// 内使用，不应该随 evidence_snapshot 持久化。
	candidatePool []candidate
}

// CompletenessClass 的取值（docs/design/topn-coefficient-convergence.md 第 3 节）。
const (
	CompletenessTight               = "tight"
	CompletenessContentRescued      = "content_rescued"
	CompletenessPoolRescued         = "pool_rescued"
	CompletenessPoolExhaustedBefore = "pool_exhausted_before_2n"
	CompletenessGapAt2N             = "gap_at_2n"
)

// PoolCandidateSnapshot is one candidate from the pre-truncation RRF-merged
// pool, carrying enough to replay ranking under an alternate
// outline_score_coefficient offline (docs/design/topn-coefficient-convergence.md
// 第 5 节) — RankByPath is this candidate's 0-based rank within each recall
// path's own ranked list (before RRF combination), keyed by path name
// ("outline"/"fts"/"fts_tuple").
type PoolCandidateSnapshot struct {
	UnitID     string         `json:"unit_id"`
	PointID    string         `json:"point_id"`
	MergedRank int            `json:"merged_rank"`
	RankByPath map[string]int `json:"rank_by_path,omitempty"`
}

// CalibrationPoolSnapshot returns the pre-truncation candidate pool (up to
// limit entries; limit<=0 means no cap) for persisting alongside a
// pool_rescued calibration sample, so Study can replay alternate
// coefficients offline without re-running recall. Returns nil when this
// EvidenceSet never carried a pool (Fast/Wiki paths, or GapReasonNoCandidates
// early-return).
func (es *EvidenceSet) CalibrationPoolSnapshot(limit int) []PoolCandidateSnapshot {
	pool := es.candidatePool
	if limit > 0 && len(pool) > limit {
		pool = pool[:limit]
	}
	if len(pool) == 0 {
		return nil
	}
	out := make([]PoolCandidateSnapshot, len(pool))
	for i, c := range pool {
		out[i] = PoolCandidateSnapshot{UnitID: c.unitID, PointID: c.pointID, MergedRank: c.mergedRank, RankByPath: c.rankByPath}
	}
	return out
}

// RRFK is rrfMerge's reciprocal-rank-fusion constant, exported so Study's
// offline coefficient replay (docs/impl/v1/topn-coefficient-convergence.md
// 阶段 C) recomputes scores identically to the live merge.
const RRFK = 60

// PathType values (docs/impl/v1/retrieval.md, docs/impl/v1/trace.md).
const (
	PathTypeFast = "fast"
	PathTypeFull = "full"
	PathTypeWiki = "wiki"
)

// ActivationHit is a link that matched during the activation layer
// (docs/impl/v1/activation.md 步骤 2 Match()), carried through to Trace so it
// can grade activation_success/activation_failure per link
// (docs/impl/v1/trace.md 步骤 3).
type ActivationHit struct {
	LinkID     string  `json:"link_id"`
	PointID    string  `json:"point_id"`
	MatchScore float64 `json:"match_score"`
	// MatchedBy is "exact" or "model" (docs/impl/v1/activation.md 步骤 2,
	// 2026-08-12) — which round of Match() produced this hit.
	MatchedBy string `json:"matched_by,omitempty"`
	// Tier/AuditSampled/Subject/Intent/Audience/Constraint (2026-08-13,
	// docs/impl/v1/activation.md「置信度分档判定」) mirror activation.LinkMatch's
	// new fields — the matched condition's own tier verdict and its own
	// stored quadruple (not the query's), populated from LinkMatch at hit
	// build time so Trace can call RecordOutcome/RecordAuditOutcome against
	// the exact condition Match() scored, without re-deriving it.
	Tier         string `json:"tier,omitempty"`
	AuditSampled bool   `json:"audit_sampled,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Intent       string `json:"intent,omitempty"`
	Audience     string `json:"audience,omitempty"`
	Constraint   string `json:"constraint,omitempty"`
}

// BundleHit is an ActivationBundle that matched during the activation layer
// (docs/impl/v1/activation-bundle.md 步骤 2 Match()), carried through to
// Trace so it can grade bundle_success/bundle_failure per bundle
// (docs/impl/v1/activation-bundle.md「验证」, 2026-08-20 阶段 2 接线) — the
// Bundle-side mirror of ActivationHit. Only bundles that actually resolved
// this round's hits (usedBundleIDs in retrieval.tryFastPath) become a
// BundleHit; a bundle that matched but lost to a higher-tier Link/Bundle
// candidate never reaches Trace, same asymmetry Link already has (Match()
// can return more candidates than resolveUnitsForPoints/resolveBundleCandidate
// end up using).
type BundleHit struct {
	BundleID   string  `json:"bundle_id"`
	MatchScore float64 `json:"match_score"`
	MatchedBy  string  `json:"matched_by,omitempty"`
	// Tier/AuditSampled/Subject/Intent/Audience/Constraint mirror
	// ActivationHit's fields — the matched condition's own tier verdict and
	// stored quadruple, populated from activation.BundleMatch at hit build
	// time so Trace can call activation.Service.RecordBundleOutcome against
	// the exact condition Match() scored.
	Tier         string `json:"tier,omitempty"`
	AuditSampled bool   `json:"audit_sampled,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Intent       string `json:"intent,omitempty"`
	Audience     string `json:"audience,omitempty"`
	Constraint   string `json:"constraint,omitempty"`
	// MemberPointIDs are the member point_ids this Bundle actually resolved
	// into this round's hits — trace.recordBundleHitOutcome calls
	// RecordMemberOutcome once per id here, not once per every member the
	// Bundle happens to carry (a member the query didn't need to resolve
	// isn't evidence either way for this particular trace).
	MemberPointIDs []string `json:"member_point_ids,omitempty"`
}

type Evidence struct {
	FactID      string          `json:"fact_id"`
	CandidateID string          `json:"-"`
	UnitID      string          `json:"unit_id"`
	PointID     string          `json:"point_id"`
	Content     string          `json:"content"`
	SourceRef   json.RawMessage `json:"source_ref"`
	Role        string          `json:"role"`
	Origin      string          `json:"origin"`
	// Attribution fields are carried to Answer so its final citation selection
	// can distinguish otherwise similar evidence from different sources,
	// products, systems, or rule scopes.
	SourceTitle  string `json:"source_title,omitempty"`
	SourceTheme  string `json:"source_theme,omitempty"`
	ContentTheme string `json:"content_theme,omitempty"`
	Object       string `json:"object,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// rerank（Rerank 直接分类产出，direct 恒为此值）/ kpn_expansion（KPN 邻居扩展补充的 supporting）
	// 供 Trace 计算 KPN 引用采纳率（study.md summary.kpn_citation_rate）
	Mined bool `json:"mined"`
	// 该证据是否为挖掘出的片段，false=整段回退（证据挖掘未实现前恒为 false，见 docs/impl/v1/evidence.md）
	// RecallPaths/MergedRank 记录该证据来源候选在召回阶段的信号，供 Trace 统计
	// outline 召回命中率与平均排名（migration 046），用于判断是否该调整
	// rrfMerge 排序权重或 rerank_top_n，而不是凭直觉调参。
	RecallPaths []string `json:"recall_paths,omitempty"`
	MergedRank  int      `json:"merged_rank"`
}

const (
	OriginRerank       = "rerank"
	OriginKPNExpansion = "kpn_expansion"
	OriginWiki         = "wiki"
)

// RoleIrrelevant marks FilteredEvidence entries — candidates the rerank
// judge classified as irrelevant rather than direct/supporting.
const RoleIrrelevant = "irrelevant"

// Gap reason values for EvidenceSet.GapReason (docs/impl/v1/retrieval.md 步骤 6).
const (
	GapReasonNoCandidates  = "no_candidates"
	GapReasonJudgeFiltered = "judge_filtered"
)

type ProgressEvent struct {
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

type ProgressFunc func(evt ProgressEvent)

type ConflictEvidence struct {
	UnitID      string          `json:"unit_id"`
	PointID     string          `json:"point_id"`
	Content     string          `json:"content"`
	SourceRef   json.RawMessage `json:"source_ref"`
	SourceTitle string          `json:"source_title"`
}

type SourceRef struct {
	SourceID  string `json:"source_id"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type candidate struct {
	candidateID   string
	unitID        string
	pointID       string
	sourceID      string
	lineStart     int
	lineEnd       int
	score         float64
	sourcePaths   []string // 角色标记，如 "direct"/"supporting"/"skeleton"；splitKeptFiltered 等阶段会覆盖
	recallOrigins []string // rrfMerge 产出的真实召回路径，如 "outline"/"fts"；一旦写入后续阶段不再覆盖，供 Evidence.RecallPaths 使用
	origin        string   // "" (rerank，默认) / OriginKPNExpansion，见 buildEvidence
	mergedRank    int      // 该候选在 rrfMerge 最终排序里的位置（0-based，topN 截断前）
	// rankByPath 是该候选在各召回路径自己的排名列表里的 0-based 排名（RRF 合并
	// 之前），键为路径名（"outline"/"fts"/"fts_tuple"）。供 Study 离线重放
	// 不同 outline_score_coefficient 时重算 RRF 分数用
	// （docs/design/topn-coefficient-convergence.md 第 5 节）。
	rankByPath map[string]int
}
