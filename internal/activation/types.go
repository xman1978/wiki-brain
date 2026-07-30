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
	// ResultExpired is concept evolution's addition to this status vocabulary
	// (docs/impl/v1/concept-evolution.md 步骤 2 过期): a pending_confirm concept
	// candidate that idled out mirrors its status onto the backing
	// learning_results row via this value.
	ResultExpired = "expired"
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
	// Concept evolution actions (docs/impl/v1/concept-evolution.md 步骤 2/3),
	// written through this package's Store since it owns learning_results.
	ActionConceptAddCandidate   = "concept_add_candidate"
	ActionConceptMergeCandidate = "concept_merge_candidate"
	ActionConceptAdd            = "concept_add"
	ActionConceptMerge          = "concept_merge"
	// ActionTopicPageCandidate is the two-tier architecture's topic-page
	// candidate audit action (docs/impl/v1/wiki.md 步骤 8, docs/impl/v1/study.md
	// 步骤 6) — object_id is the shell page's page_id (identity is inherently
	// unique; the confirmation target is a concrete page, not a concept id or
	// member-set fingerprint).
	ActionTopicPageCandidate = "topic_page_candidate"
)

// learning_results.object_type — activation_link is written by this module;
// knowledge_gap / wiki_page are Study's own audit objects (docs/impl/v1/study.md
// 步骤 6), written through this package's Store.InsertLearningResult since it
// owns the shared learning_results table. concept_candidate is concept
// evolution's own audit object (docs/impl/v1/concept-evolution.md).
const (
	ObjectTypeActivationLink   = "activation_link"
	ObjectTypeKnowledgeGap     = "knowledge_gap"
	ObjectTypeWikiPage         = "wiki_page"
	ObjectTypeConceptCandidate = "concept_candidate"
)

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

// LinkCondition is what Study / CreateLink pass in. ObservedConditions is the
// Match truth source (docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md).
// Legacy SubjectTerms / IntentTerms / Audience / ConstraintTerms are only used
// when ObservedConditions is empty (transitional callers / tests) via
// EffectiveConditions(), then projected back onto the link row for old UI.
type LinkCondition struct {
	ObservedConditions []ObservedCondition
	SubjectTerms       string
	IntentTerms        []string
	Audience           []string
	ConstraintTerms    []string
}

type ActivationLink struct {
	LinkID              string
	QuestionTerms       string
	SubjectTerms        string
	IntentTerms         []string
	Audience            []string
	ConstraintTerms     []string
	ObservedConditions  []ObservedCondition
	Scene               string
	Goal                string
	PointID             string
	Status              string
	AdoptCount          int
	FailCount           int
	LastUsedAt          sql.NullTime
	CreatedFrom         string
	StatusChangedAt     sql.NullTime
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
