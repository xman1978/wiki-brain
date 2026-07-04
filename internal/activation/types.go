package activation

import (
	"database/sql"
	"time"
)

// ActivationLink status machine (docs/impl/v1/activation.md 状态机).
// conflicted is a reserved enum value the doc keeps for V2; V1 never
// produces it.
const (
	StatusCandidate  = "candidate"
	StatusVerified   = "verified"
	StatusWeakened   = "weakened"
	StatusDeprecated = "deprecated"
)

// learning_results.status
const (
	ResultApplied        = "applied"
	ResultPendingConfirm = "pending_confirm"
	ResultRejected       = "rejected"
)

// learning_results.action
const (
	ActionCreateCandidate = "create_candidate"
	ActionPromote         = "promote"
	ActionWeaken          = "weaken"
	ActionReverify        = "reverify"
	ActionDeprecate       = "deprecate"
	ActionGapFlag         = "gap_flag"
	ActionWikiCandidate   = "wiki_candidate"
	ActionRecompileFlag   = "recompile_flag"
)

// ObjectTypeActivationLink is the learning_results.object_type value used by
// every action this module records.
const ObjectTypeActivationLink = "activation_link"

// legalTransitions is the only source of truth for which status moves are
// allowed (docs/impl/v1/activation.md "合法迁移表"). deprecated is terminal.
var legalTransitions = map[string]map[string]bool{
	StatusCandidate: {StatusVerified: true, StatusDeprecated: true},
	StatusVerified:  {StatusWeakened: true},
	StatusWeakened:  {StatusVerified: true, StatusDeprecated: true},
}

// transitionAction maps a legal (from, to) pair to its learning_results
// action label.
var transitionAction = map[string]map[string]string{
	StatusCandidate: {StatusVerified: ActionPromote, StatusDeprecated: ActionDeprecate},
	StatusVerified:  {StatusWeakened: ActionWeaken},
	StatusWeakened:  {StatusVerified: ActionReverify, StatusDeprecated: ActionDeprecate},
}

// LinkCondition is the activation condition quadruple, already normalized by
// the caller (Study, from a confident trace's subject/intent/audience/
// constraint) — CreateLink stores it as-is (docs/impl/v1/activation.md 数据结构).
type LinkCondition struct {
	SubjectTerms    string
	IntentTerms     string
	Audience        string
	ConstraintTerms string
}

type ActivationLink struct {
	LinkID          string
	QuestionTerms   string
	SubjectTerms    string
	IntentTerms     string
	Audience        string
	ConstraintTerms string
	Scene           string
	Goal            string
	PointID         string
	Status          string
	AdoptCount      int
	FailCount       int
	LastUsedAt      sql.NullTime
	CreatedFrom     string
	StatusChangedAt sql.NullTime
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ActivationLinkListRow is ActivationLink plus the KP/KU join fields the
// management list API exposes (docs/impl/v1/activation.md 步骤 3).
type ActivationLinkListRow struct {
	ActivationLink
	PointSummary string
	UnitCenter   string
}

type LearningResult struct {
	ResultID    string
	Action      string
	ObjectType  string
	ObjectID    string
	Reason      string
	EventIDs    string
	Status      string
	ConfirmedBy sql.NullString
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// LinkMatch is a single Match() result (docs/impl/v1/activation.md 步骤 2).
type LinkMatch struct {
	Link  ActivationLink
	Score float64
}
