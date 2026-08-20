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
}

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
}
