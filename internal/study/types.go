package study

import "time"

type GapEvent struct {
	EventID       string
	TraceID       string
	Question      string
	QuestionTerms string
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
	GapID         string
	QuestionTerms string
	Question      string
	HitCount      int
}

type TracePathRow struct {
	Path              string
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
}

type TraceSummary struct {
	TotalTraces            int     `json:"total_traces"`
	ConfidentCount         int     `json:"confident_count"`
	PartialCount           int     `json:"partial_count"`
	GapCount               int     `json:"gap_count"`
	ConfidentRate          float64 `json:"confident_rate"`
	TotalCooccurrencePairs int     `json:"total_cooccurrence_pairs"`
	CandidatesFlagged      int     `json:"candidates_flagged"`
}

type ActivationLinkCandidate struct {
	QuestionTerms  string                    `json:"question_terms"`
	PointID        string                    `json:"point_id"`
	PointSummary   string                    `json:"point_summary"`
	UnitTopic      string                    `json:"unit_topic"`
	ConceptID      string                    `json:"concept_id"`
	ConceptName    string                    `json:"concept_name"`
	Stats          ActivationLinkStats       `json:"stats"`
	Recommendation string                    `json:"recommendation"`
	Reason         string                    `json:"reason"`
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
	QuestionTerms  string `json:"question_terms"`
	Question       string `json:"question"`
	HitCount       int    `json:"hit_count"`
	Recommendation string `json:"recommendation"`
}

type RunResult struct {
	ReportID           string `json:"report_id"`
	CandidatesFlagged  int    `json:"candidates_flagged"`
	GapEventsProcessed int    `json:"gap_events_processed"`
	ElapsedMs          int64  `json:"elapsed_ms"`
}
