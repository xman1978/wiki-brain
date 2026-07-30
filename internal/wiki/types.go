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
	PageID             string
	PageType           string
	ConceptID          sql.NullString
	Title              string
	Content            string
	Status             string
	SourcePointIDs     string // JSON array
	SourceUnitIDs      string // JSON array
	SourceLinkIDs      string // JSON array — verified ActivationLink ids covering the cited KPs at compile time
	ObservedConditions string // JSON array of activation.ObservedCondition — union of cited KPs' verified-link
	// conditions, read-only consumption for the four-tuple retrieval entry (docs/design/wiki-compilation.md
	// "触发问法取材真实观测，检索匹配复用四元组"); does not drive promote/weaken statistics.
	Aliases          string // JSON array — concept aliases/abbreviations, index-only (not citable content)
	TriggerQuestions string // JSON array — typical questions this page answers, index-only (not citable content)
	MemberRoles      string // JSON array of MemberRole — topic pages only (concept pages always "[]")
	UncoveredPoints  string // JSON array of UncoveredPoint — field-only, never enters body/citation whitelist/gates
	CompiledFrom     string // JSON array — learning_result / report ids that triggered this (re)compile
	PromptVersion    string
	ModelName        string
	CompiledAt       sql.NullTime
	PublishedAt      sql.NullTime
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Revision struct {
	RevisionID string
	PageID     string
	Content    string
	Reason     string
	CreatedAt  time.Time
}

// CompileRequest is POST /wiki/compile's request body
// (docs/impl/v1/wiki.md 步骤 2). Claims/Tensions are optional: when present
// (the caller round-tripped an /wiki/compile/analyze response back, possibly
// human-edited), generation is constrained to them directly; when absent,
// Compile runs the analysis step internally before generating.
type CompileRequest struct {
	ConceptID string    `json:"concept_id"`
	PageType  string    `json:"page_type"`
	ResultID  string    `json:"result_id,omitempty"`
	Claims    []Claim   `json:"claims,omitempty"`
	Tensions  []Tension `json:"tensions,omitempty"`
}

// AnalyzeRequest is POST /wiki/compile/analyze's request body
// (docs/impl/v1/wiki.md 步骤 2). Same shape as CompileRequest minus the
// claims/tensions fields, which this endpoint produces rather than consumes.
type AnalyzeRequest struct {
	ConceptID string `json:"concept_id"`
	PageType  string `json:"page_type"`
	ResultID  string `json:"result_id,omitempty"`
}

// AnalyzeResult is POST /wiki/compile/analyze's response — never persisted
// (docs/design/wiki-compilation.md "编译内部分两步"). The caller holds it
// and, if the human confirms, sends it back verbatim (or edited) as
// CompileRequest.Claims/Tensions.
type AnalyzeResult struct {
	ConceptID string    `json:"concept_id"`
	PageType  string    `json:"page_type"`
	ResultID  string    `json:"result_id,omitempty"`
	Claims    []Claim   `json:"claims"`
	Tensions  []Tension `json:"tensions"`
}

// Claim is one analysis-stage stable-conclusion candidate: a core idea plus
// the point_ids it cites, fixed before generation narrows what the
// generation prompt is allowed to reference (docs/impl/v1/wiki.md 步骤 3
// "分析产物"). Not the same as the design doc's persisted Claim object (V2) —
// this one is a compile-time intermediate, never stored.
type Claim struct {
	Summary       string   `json:"summary"`
	CitedPointIDs []string `json:"cited_point_ids"`
}

// Tension is one analysis-stage material conflict / open question candidate
// for the generated page's "待验证点" section.
type Tension struct {
	Description     string   `json:"description"`
	RelatedPointIDs []string `json:"related_point_ids"`
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

// wiki_page_relations.relation_type — only these three, no broader/narrower
// (docs/design/wiki-compilation.md "页面关系只有三种，层级由 contains 承载").
const (
	RelationRelated     = "related"
	RelationContradicts = "contradicts"
	RelationContains    = "contains"
)

// wiki_page_relations.derived_from
const (
	DerivedFromKPN     = "kpn"
	DerivedFromCompile = "compile"
)

// PageRelation is a wiki_page_relations row.
type PageRelation struct {
	RelationID   string
	FromPageID   string
	ToPageID     string
	RelationType string
	DerivedFrom  string
	Evidence     string // JSON: {"shared_point_ids":[...],"kpn_relation_count":N}; "{}" for contains
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RelationEvidence is the decoded form of PageRelation.Evidence for
// related/contradicts rows (docs/impl/v1/wiki.md 步骤 7).
type RelationEvidence struct {
	SharedPointIDs   []string `json:"shared_point_ids"`
	KPNRelationCount int      `json:"kpn_relation_count"`
}

// MemberRole is one wiki_pages.member_roles entry (topic pages only) —
// structured form of the "子主题分工" section, so V3 decomposition can query
// it directly instead of re-parsing Markdown (docs/impl/v1/wiki.md 步骤 8).
type MemberRole struct {
	MemberPageID  string   `json:"member_page_id"`
	Aspect        string   `json:"aspect"`
	QuestionTypes []string `json:"question_types"`
}

// UncoveredPoint is one wiki_pages.uncovered_points entry — a KP in the
// page's topic scope that is lifecycle=current but not yet qualifying (no
// verified ActivationLink). Field-only: never enters body, citation
// whitelist, or any compile gate (docs/impl/v1/wiki.md "数据结构").
type UncoveredPoint struct {
	PointID string `json:"point_id"`
	Summary string `json:"summary"`
}

// Draft is a wiki_drafts row (docs/impl/v1/wiki.md 步骤 10) — a page-derived
// writing surface that is freely editable and never written back to
// wiki_pages.
type Draft struct {
	DraftID          string
	PageID           string
	SourceRevisionID string
	SourcePageIDs    string // JSON array
	EvidenceIndex    string // JSON array of EvidenceIndexEntry
	Title            string
	Content          string
	Note             string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// wiki_drafts mode (POST /wiki/pages/:id/drafts request, not persisted as a
// column — it only determines how source_page_ids/content get assembled).
const (
	DraftModePage      = "page"
	DraftModeAssembled = "assembled"
)

// EvidenceIndexEntry is one wiki_drafts.evidence_index item — read-only,
// generated at draft-creation time, never changes with manual edits.
type EvidenceIndexEntry struct {
	PointID      string    `json:"point_id"`
	PointSummary string    `json:"point_summary"`
	UnitID       string    `json:"unit_id"`
	UnitTopic    string    `json:"unit_topic"`
	SourceRef    SourceRef `json:"source_ref"`
}

// SourceRef mirrors evidence.SourceRef's JSON shape without importing the
// evidence package (docs/impl/v1/wiki.md 步骤 10).
type SourceRef struct {
	SourceID  string `json:"source_id"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
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
