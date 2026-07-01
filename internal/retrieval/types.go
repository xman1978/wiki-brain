package retrieval

import "encoding/json"

type QueryContext struct {
	Question   string
	Subject    string
	Intent     string
	Audience   string
	Constraint string
}

type EvidenceSet struct {
	Question       string             `json:"question"`
	Subject        string             `json:"subject,omitempty"`
	Audience       string             `json:"audience,omitempty"`
	Constraint     string             `json:"constraint,omitempty"`
	Path           string             `json:"path"`
	DirectEvidence []Evidence         `json:"direct_evidence"`
	Supporting     []Evidence         `json:"supporting"`
	Conflicts      []ConflictEvidence `json:"conflicts,omitempty"`
}

type Evidence struct {
	FactID      string          `json:"fact_id"`
	CandidateID string          `json:"-"`
	UnitID      string          `json:"unit_id"`
	PointID     string          `json:"point_id"`
	Content     string          `json:"content"`
	SourceRef   json.RawMessage `json:"source_ref"`
	Role        string          `json:"role"`
}

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
	candidateID string
	unitID      string
	pointID     string
	sourceID    string
	lineStart   int
	lineEnd     int
	score       float64
	sourcePaths []string // "outline", "fts"
}
