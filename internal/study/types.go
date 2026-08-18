package study

import (
	"time"

	"github.com/jxman78/wiki-brain/internal/entry"
)

type GapEvent struct {
	EventID       string
	TraceID       string
	Question      string
	QuestionTerms string
	Reason        string
}

type LinkCandidateRow struct {
	CandidateID    string
	QuestionTerms  string
	PointID        string
	ConfidentCount int
	HitCount       int
	PointSummary   string
	UnitTopic      string
	EntryID        string
	ConceptName    string
}

type QualifyingKP struct {
	PointID        string
	PointSummary   string
	ConfidentCount int
}

type KnowledgeGapRow struct {
	GapID            string
	QuestionTerms    string
	Question         string
	HitCount         int
	ReasonCountsJSON string
	LastReason       string
	LastTraceID      string
}

type TracePathRow struct {
	Path               string
	DirectPointIDsJSON string
}

type ReportMeta struct {
	ReportID        string    `json:"report_id"`
	PeriodDays      int       `json:"period_days"`
	CandidatesCount int       `json:"candidates_count"`
	WikiCount       int       `json:"wiki_count"`
	GapCount        int       `json:"gap_count"`
	CreatedAt       time.Time `json:"created_at"`
}

// Report JSON structure

type Report struct {
	ReportID    string    `json:"report_id"`
	GeneratedAt time.Time `json:"generated_at"`
	PeriodDays  int       `json:"period_days"`

	Summary                  TraceSummary              `json:"summary"`
	ActivationLinkCandidates []ActivationLinkCandidate `json:"activation_link_candidates"`
	KnowledgeGaps            []KnowledgeGapEntry       `json:"knowledge_gaps"`
	LearningActions          LearningActionsSummary    `json:"learning_actions"`
	CrossSourceConflicts     []CrossSourceConflict     `json:"cross_source_conflicts"`
	EntryCandidates          EntryCandidatesSection    `json:"entry_candidates"`

	// wiki-single-tier-revision.md: 概念页 qualifying 自动标记与主题四元组
	// 聚类均已删除（改为人工指定 entry_id 触发编译），原 wiki_candidates /
	// topic_signal_underfilled / entry_split_signals 报告节随之移除。
	WikiDraftReflow    []WikiDraftReflowEntry    `json:"wiki_draft_reflow,omitempty"`
	QuestionComplexity QuestionComplexitySection `json:"question_complexity"`

	// Convergence is the 收敛趋势 report section (docs/impl/v1/study.md 步骤
	// 7, docs/design/activation-convergence.md 第 5 节): this cycle's
	// confidence-width/tier snapshot plus a trend against recent prior
	// reports — is the distribution narrowing, is the exploration-budget
	// share going down.
	Convergence ConvergenceSection `json:"convergence"`
}

// ConvergenceSection is generateReport's "convergence" report item.
type ConvergenceSection struct {
	Current ConvergenceStats `json:"current"`
	// Trend compares Current against up to N previous reports' snapshots
	// (oldest first), so a reader can see whether AvgWidth/WideCount/tier
	// mix are moving in the intended direction over time rather than just
	// seeing one point-in-time number.
	Trend []ConvergenceTrendPoint `json:"trend"`
}

// ConvergenceTrendPoint is one historical report's convergence snapshot,
// reduced to its ReportID/GeneratedAt plus the same ConvergenceStats shape.
type ConvergenceTrendPoint struct {
	ReportID    string           `json:"report_id"`
	GeneratedAt time.Time        `json:"generated_at"`
	Stats       ConvergenceStats `json:"stats"`
}

// WikiDraftReflowEntry is one origin=wiki_draft source's reflow footprint
// (docs/impl/v1/wiki.md 步骤 10「可观测」).
type WikiDraftReflowEntry struct {
	SourceID             string `json:"source_id"`
	OriginPageID         string `json:"origin_page_id"`
	ProducedKPCount      int    `json:"produced_kp_count"`
	SkippedAncestorEdges int    `json:"skipped_ancestor_edges"`
}

// QuestionComplexitySection is docs/impl/v1/study.md 步骤 7's "问题复杂度观测
// 量" — observe-only, never feeds any online routing decision (V1 has no
// routing layer).
type QuestionComplexitySection struct {
	Groups []QuestionComplexityGroup `json:"groups"`
}

type QuestionComplexityGroup struct {
	Subject             string         `json:"subject"`
	Intent              string         `json:"intent"`
	Audience            string         `json:"audience"`
	Constraint          string         `json:"constraint"`
	QuestionCount       int            `json:"question_count"`
	PathDistribution    map[string]int `json:"path_distribution"`
	AvgDirectPointCount float64        `json:"avg_direct_point_count"`
	WikiSatisfiedRatio  float64        `json:"wiki_satisfied_ratio"`
	// ComplexityHint stays nil until thresholds are calibrated against real
	// traces (docs/impl/v1/study.md 步骤 7: "阈值不预先拍定").
	ComplexityHint *string `json:"complexity_hint"`
}

// EntryCandidatesSection is the study report's concept evolution section
// (docs/impl/v1/concept-evolution.md 步骤 5): this cycle's scan counts plus
// the currently pending candidates, audited alongside the window's
// entry_gap signal volume.
type EntryCandidatesSection struct {
	AddCreated         int                   `json:"add_created"`
	AddUpdated         int                   `json:"add_updated"`
	MergeCreated       int                   `json:"merge_created"`
	MergeUpdated       int                   `json:"merge_updated"`
	Expired            int                   `json:"expired"`
	EntryGapEventCount int                   `json:"entry_gap_event_count"`
	PendingAdd         []entry.CandidateView `json:"pending_add"`
	PendingMerge       []entry.CandidateView `json:"pending_merge"`
}

// CrossSourceConflict is a read-only, display-only entry (docs/impl/v1/kpn.md
// 步骤 5) — no automatic action is taken on it.
type CrossSourceConflict struct {
	RelationID string            `json:"relation_id"`
	PointA     ConflictPointInfo `json:"point_a"`
	PointB     ConflictPointInfo `json:"point_b"`
	CreatedAt  time.Time         `json:"created_at"`
}

type ConflictPointInfo struct {
	PointID     string `json:"point_id"`
	Content     string `json:"content"`
	SourceTitle string `json:"source_title"`
}

type TraceSummary struct {
	TotalTraces            int     `json:"total_traces"`
	ConfidentCount         int     `json:"confident_count"`
	PartialCount           int     `json:"partial_count"`
	GapCount               int     `json:"gap_count"`
	ConfidentRate          float64 `json:"confident_rate"`
	TotalCooccurrencePairs int     `json:"total_cooccurrence_pairs"`
	CandidatesFlagged      int     `json:"candidates_flagged"`
	KPNCitedCount          int     `json:"kpn_cited_count"`
	CitedCount             int     `json:"cited_count"`
	KPNCitationRate        float64 `json:"kpn_citation_rate"`
	// kpn_citation_rate = kpn_cited_count / cited_count（窗口内 Answer 实际引用的证据中，
	// 来自 KPN 扩展而非 Rerank 直接产出的比例；cited_count = 0 时为 0，见 unit.md KPN 关系类型收窄的设计决策）
	OutlineCitedCount   int     `json:"outline_cited_count"`
	CitedRankSum        int     `json:"cited_rank_sum"`
	OutlineCitationRate float64 `json:"outline_citation_rate"`
	CitedAvgRank        float64 `json:"cited_avg_rank"`
	// outline_citation_rate = outline_cited_count / cited_count（窗口内 Answer 实际引用的证据中，
	// 来自目录结构召回的比例）；cited_avg_rank = cited_rank_sum / cited_count（被引用证据在
	// RRF 合并列表中的平均排名，0-based，rerank_top_n 截断前）；两者都是 0 当 cited_count = 0。
	// 用于判断 outline 召回是否确实比 FTS 排名更靠前、以及 rerank_top_n 是否有下调空间
	// （2026-08-09 决策，见对话记录，不是凭直觉调参）
	FastPathRate float64 `json:"fast_path_rate"`
	// 窗口内 traces.path_type = fast 的占比，验证「学习改变检索行为」的 V1 目标
}

type ActivationLinkCandidate struct {
	QuestionTerms  string              `json:"question_terms"`
	PointID        string              `json:"point_id"`
	PointSummary   string              `json:"point_summary"`
	UnitTopic      string              `json:"unit_topic"`
	EntryID        string              `json:"entry_id"`
	ConceptName    string              `json:"entry_name"`
	Stats          ActivationLinkStats `json:"stats"`
	Recommendation string              `json:"recommendation"`
	Reason         string              `json:"reason"`
}

type ActivationLinkStats struct {
	ConfidentCount    int       `json:"confident_count"`
	HitCount          int       `json:"hit_count"`
	SignalPurity      float64   `json:"signal_purity"`
	ActivationBreadth int       `json:"activation_breadth"`
	ShortPathRate     float64   `json:"short_path_rate"`
	HasKPNNeighbors   bool      `json:"has_kpn_neighbors"`
	LastSeenAt        time.Time `json:"last_seen_at"`
}

type KnowledgeGapEntry struct {
	QuestionTerms  string         `json:"question_terms"`
	Question       string         `json:"question"`
	HitCount       int            `json:"hit_count"`
	ReasonCounts   map[string]int `json:"reason_counts"`
	LastReason     string         `json:"last_reason,omitempty"`
	LastTraceID    string         `json:"last_trace_id,omitempty"`
	Recommendation string         `json:"recommendation"`
}

type RunResult struct {
	ReportID           string                 `json:"report_id"`
	CandidatesFlagged  int                    `json:"candidates_flagged"`
	GapEventsProcessed int                    `json:"gap_events_processed"`
	LearningActions    LearningActionsSummary `json:"learning_actions"`
	ElapsedMs          int64                  `json:"elapsed_ms"`
}

// LearningActionsSummary is the report/run-response section documenting what
// this cycle's learning actions did (docs/impl/v1/study.md 步骤 7).
type LearningActionsSummary struct {
	CreatedCandidates int `json:"created_candidates"`
	// Promoted/PendingPromotions/Weakened/Reverified/Deprecated removed
	// (2026-08-13, docs/design/activation-convergence.md 第 9 节): no more
	// discrete state transitions to count — replaced by PrunedConditions
	// below (docs/impl/v1/study.md 步骤 3「收敛剪枝」).
	// SynonymCandidatesCreated counts subject_synonyms rows created this cycle
	// (candidate or, when synonym_auto_promote=true, active) from
	// subject_synonym_gap aggregation (docs/impl/v1/study.md 步骤 2a).
	SynonymCandidatesCreated int `json:"synonym_candidates_created"`
	// PrunedConditions is the total number of observed_conditions entries
	// removed this cycle across all links by pruneConditions (docs/impl/v1/
	// study.md 步骤 3).
	PrunedConditions int                   `json:"pruned_conditions"`
	Actions          []LearningActionEntry `json:"actions"`
}

type LearningActionEntry struct {
	ResultID string `json:"result_id"`
	Action   string `json:"action"`
	ObjectID string `json:"object_id"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
}

// RawSignalEvent is a learning_events row of one of the three activation
// signal types, joined against traces for question_hash — the input to
// aggregateSignals (docs/impl/v1/study.md 步骤 3).
type RawSignalEvent struct {
	EventID      string
	EventType    string
	Payload      string
	Processed    int
	CreatedAt    time.Time
	QuestionHash string
}

// linkSignal is the per-link_id aggregation result of aggregateSignals: the
// window (or batch, depending on which event slice was aggregated) counts
// used for threshold judgment and stat updates.
type linkSignal struct {
	// SuccessDirectN / SuccessSupportingN split activation_success events by
	// payload.role (docs/impl/v1/trace.md 步骤 3). Promotion (candidate →
	// verified) and the weaken-ratio denominator only count SuccessDirectN
	// — repeatedly serving as supporting evidence alone doesn't prove a link
	// can be trusted as an independent activation entry (docs/design/
	// precompile.md "反复使用"). Reverify (weakened → verified) and the
	// adopt_count stat count both roles: both are real, confirmed use.
	SuccessDirectN     int
	SuccessSupportingN int
	DistinctN          int // distinct question hashes among role=direct successes only
	FailureN           int
	EventIDs           []string
	distinctHashes     map[string]bool
}

// SuccessTotal is direct+supporting successes — used for adopt_count and
// reverify, where any confirmed real use counts (docs/impl/v1/study.md 步骤3).
func (l *linkSignal) SuccessTotal() int {
	return l.SuccessDirectN + l.SuccessSupportingN
}

// LearningResultRow is a learning_results row plus, for object_type=
// activation_link, the joined question_terms/point_summary the management
// view needs (docs/impl/v1/study.md 步骤 8).
type LearningResultRow struct {
	ResultID      string    `json:"result_id"`
	Action        string    `json:"action"`
	ObjectType    string    `json:"object_type"`
	ObjectID      string    `json:"object_id"`
	Reason        string    `json:"reason"`
	EventIDs      []string  `json:"event_ids"`
	Status        string    `json:"status"`
	ConfirmedBy   string    `json:"confirmed_by,omitempty"`
	QuestionTerms string    `json:"question_terms,omitempty"`
	PointSummary  string    `json:"point_summary,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// LearningResultDetail is GET /study/results/:id's response: the full row
// plus a summary of each backing learning_event (docs/impl/v1/study.md 步骤 8).
type LearningResultDetail struct {
	LearningResultRow
	Events []LearningEventSummary `json:"events"`
}

type LearningEventSummary struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}
