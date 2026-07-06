// Package wiki implements docs/impl/v1/wiki.md: compiling Study-identified
// concept/topic candidates into published pages with evidence back-links,
// and serving them as Retrieval's Wiki direct-answer layer.
package wiki

import (
	"database/sql"
	"errors"
	"time"
)

// Business error sentinels — handler.go maps these to specific HTTP statuses.
var (
	ErrPageAlreadyExists      = errors.New("wiki: a non-archived page already exists for this concept")
	ErrPageNotFound           = errors.New("wiki: page not found")
	ErrPageArchived           = errors.New("wiki: page is archived")
	ErrInvalidStateTransition = errors.New("wiki: invalid page state transition")
)

// Page.page_type — V1 only distinguishes title framing (see compile prompt's
// {{page_type_hint}}); the compile input is identical either way.
const (
	PageTypeTopic   = "topic"
	PageTypeConcept = "concept"
)

// Page.status
const (
	StatusDraft          = "draft"
	StatusPublished      = "published"
	StatusNeedsRecompile = "needs_recompile"
	StatusArchived       = "archived"
)

type Page struct {
	PageID         string
	PageType       string
	ConceptID      sql.NullString
	Title          string
	Content        string
	Status         string
	SourcePointIDs string // JSON array
	SourceUnitIDs  string // JSON array
	CompiledFrom   string // JSON array — learning_result / report ids that triggered this (re)compile
	PromptVersion  string
	ModelName      string
	CompiledAt     sql.NullTime
	PublishedAt    sql.NullTime
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Revision struct {
	RevisionID string
	PageID     string
	Content    string
	Reason     string
	CreatedAt  time.Time
}

// CompileRequest is POST /wiki/compile's request body
// (docs/impl/v1/wiki.md 步骤 2).
type CompileRequest struct {
	ConceptID string `json:"concept_id"`
	PageType  string `json:"page_type"`
	ResultID  string `json:"result_id,omitempty"`
}

// QualifyingPoint is one compile-input KP: content plus enough to locate its
// KU/Source for the "依赖来源" section and for source_unit_ids reverse-lookup.
type QualifyingPoint struct {
	PointID        string
	UnitID         string
	SourceID       string
	Content        string
	UnitCenter     string
	LineStart      int
	LineEnd        int
	ConfidentCount int
}

// GapCandidate is a knowledge_gaps row considered for a compile's "待验证点"
// material (docs/impl/v1/wiki.md 步骤 3).
type GapCandidate struct {
	QuestionTerms string
	Question      string
	HitCount      int
}

// DirectAnswerResult is what TryDirectAnswer hands back to Retrieval
// (docs/impl/v1/wiki.md 步骤 4) — evidence_snapshot only ever records
// {wiki_page_id, cited_point_ids}; Content is a side channel Answer consumes
// directly (never itself persisted to evidence_snapshot).
type DirectAnswerResult struct {
	PageID        string
	Content       string
	CitedPointIDs []string
}
