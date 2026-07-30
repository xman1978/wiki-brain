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
	HasFeedback       bool      `json:"has_feedback"`
	FeedbackType      string    `json:"feedback_type,omitempty"`
	FeedbackContent   string    `json:"feedback_content,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
	// SkeletonPageID is non-empty when a topic page provided this question's
	// recall skeleton (docs/impl/v1/wiki.md 步骤 8「检索接入」两层架构扩展),
	// regardless of which path_type the answer ultimately took.
	SkeletonPageID string `json:"skeleton_page_id,omitempty"`
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
