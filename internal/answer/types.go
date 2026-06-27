package answer

import (
	"time"

	"github.com/jxman78/wiki-brain/internal/retrieval"
)

type AnswerResult struct {
	AnswerID    string               `json:"answer_id"`
	Question    string               `json:"question"`
	Content     string               `json:"content"`
	Citations   []string             `json:"citations"`
	HasAnswer   bool                 `json:"has_answer"`
	Path        string               `json:"path"`
	EvidenceSet *retrieval.EvidenceSet `json:"evidence_snapshot"`
	CreatedAt   time.Time            `json:"created_at,omitempty"`
}

type llmAnswerOutput struct {
	Content   string   `json:"content"`
	Citations []string `json:"citations"`
}
