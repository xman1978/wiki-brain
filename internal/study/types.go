package study

import (
	"time"

	"github.com/jxman78/wiki-brain/internal/concept"
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
	ConceptID      string
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
	WikiCandidates           []WikiCandidate           `json:"wiki_candidates"`
	KnowledgeGaps            []KnowledgeGapEntry       `json:"knowledge_gaps"`
	LearningActions          LearningActionsSummary    `json:"learning_actions"`
	CrossSourceConflicts     []CrossSourceConflict     `json:"cross_source_conflicts"`
	ConceptCandidates        ConceptCandidatesSection  `json:"concept_candidates"`
}

// ConceptCandidatesSection is the study report's concept evolution section
// (docs/impl/v1/concept-evolution.md 步骤 5): this cycle's scan counts plus
// the currently pending candidates, audited alongside the window's
// concept_gap signal volume.
type ConceptCandidatesSection struct {
	AddCreated           int                     `json:"add_created"`
	AddUpdated           int                     `json:"add_updated"`
	MergeCreated         int                     `json:"merge_created"`
	MergeUpdated         int                     `json:"merge_updated"`
	Expired              int                     `json:"expired"`
	ConceptGapEventCount int                     `json:"concept_gap_event_count"`
	PendingAdd           []concept.CandidateView `json:"pending_add"`
	PendingMerge         []concept.CandidateView `json:"pending_merge"`
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
	FastPathRate float64 `json:"fast_path_rate"`
	// 窗口内 traces.path_type = fast 的占比，验证「学习改变检索行为」的 V1 目标
}

type ActivationLinkCandidate struct {
	QuestionTerms  string              `json:"question_terms"`
	PointID        string              `json:"point_id"`
	PointSummary   string              `json:"point_summary"`
	UnitTopic      string              `json:"unit_topic"`
	ConceptID      string              `json:"concept_id"`
	ConceptName    string              `json:"concept_name"`
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

type WikiCandidate struct {
	ConceptID          string                `json:"concept_id"`
	ConceptName        string                `json:"concept_name"`
	DomainID           string                `json:"domain_id"`
	QualifyingPointIDs []string              `json:"qualifying_point_ids"`
	QualifyingPoints   []WikiQualifyingPoint `json:"qualifying_points"`
	Stats              WikiCandidateStats    `json:"stats"`
	Recommendation     string                `json:"recommendation"`
	Reason             string                `json:"reason"`
}

type WikiQualifyingPoint struct {
	PointID        string `json:"point_id"`
	PointSummary   string `json:"point_summary"`
	ConfidentCount int    `json:"confident_count"`
}

type WikiCandidateStats struct {
	QualifyingKPCount  int     `json:"qualifying_kp_count"`
	AvgConfidentCount  float64 `json:"avg_confident_count"`
	KPNConnectionCount int     `json:"kpn_connection_count"`
	DaysActive         int     `json:"days_active"`
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
	CreatedCandidates int                   `json:"created_candidates"`
	Promoted          int                   `json:"promoted"`
	PendingPromotions int                   `json:"pending_promotions"`
	Weakened          int                   `json:"weakened"`
	Reverified        int                   `json:"reverified"`
	Deprecated        int                   `json:"deprecated"`
	Actions           []LearningActionEntry `json:"actions"`
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
	SuccessN       int
	DistinctN      int
	FailureN       int
	EventIDs       []string
	distinctHashes map[string]bool
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
