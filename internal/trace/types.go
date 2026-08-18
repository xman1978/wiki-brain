package trace

import "time"

type Trace struct {
	TraceID           string    `json:"trace_id"`
	AnswerID          string    `json:"answer_id"`
	Question          string    `json:"question"`
	QuestionHash      string    `json:"question_hash,omitempty"`
	QuestionTerms     string    `json:"question_terms,omitempty"`
	RetrievalQuality  string    `json:"retrieval_quality"`
	Path              string    `json:"path,omitempty"`
	PathType          string    `json:"path_type,omitempty"`
	ActivationLinkIDs []string  `json:"activation_link_ids,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	Intent            string    `json:"intent,omitempty"`
	Audience          string    `json:"audience,omitempty"`
	ConstraintText    string    `json:"constraint_text,omitempty"`
	DirectPointIDs    []string  `json:"direct_point_ids,omitempty"`
	KPNCitedCount     int       `json:"kpn_cited_count"`
	CitedCount        int       `json:"cited_count"`
	OutlineCitedCount int       `json:"outline_cited_count"`
	CitedRankSum      int       `json:"cited_rank_sum"`
	HasFeedback       bool      `json:"has_feedback"`
	FeedbackType      string    `json:"feedback_type,omitempty"`
	FeedbackContent   string    `json:"feedback_content,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

// PointSummary is a knowledge_point's identity + content, used by
// resolvePointBinding to present an ambiguous unit's candidate KPs to the LLM.
type PointSummary struct {
	PointID string
	UnitID  string
	Content string
}

type Cooccurrence struct {
	QuestionTerms  string    `json:"question_terms"`
	PointID        string    `json:"point_id"`
	HitCount       int       `json:"hit_count"`
	ConfidentCount int       `json:"confident_count"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type LearningEvent struct {
	EventID   string    `json:"event_id"`
	TraceID   string    `json:"trace_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
	Processed int       `json:"processed"`
	CreatedAt time.Time `json:"created_at"`
}

type FeedbackRequest struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}
