package trace

import "time"

type Trace struct {
	TraceID          string    `json:"trace_id"`
	AnswerID         string    `json:"answer_id"`
	Question         string    `json:"question"`
	QuestionHash     string    `json:"question_hash,omitempty"`
	QuestionTerms    string    `json:"question_terms,omitempty"`
	RetrievalQuality string    `json:"retrieval_quality"`
	Path             string    `json:"path,omitempty"`
	DirectPointIDs   []string  `json:"direct_point_ids,omitempty"`
	HasFeedback      bool      `json:"has_feedback"`
	FeedbackType     string    `json:"feedback_type,omitempty"`
	FeedbackContent  string    `json:"feedback_content,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
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
