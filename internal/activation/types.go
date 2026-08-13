package activation

import (
	"database/sql"
	"time"
)

// ActivationLink status (docs/impl/v1/activation.md 状态机, 2026-08-13 起三态:
// weakened 整体退休——连续置信度下 mean(cond) 本身就连续表达"正在变差"，不再
// 需要一个额外的中间标签). status is a derived/cached field (see
// Service.deriveAndPersistStatus), not a state machine driven by legal
// transitions. conflicted is a reserved enum value the doc keeps for V2; V1
// never produces it.
const (
	StatusCandidate  = "candidate"
	StatusVerified   = "verified"
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
	// ActionPruneCondition replaces promote/weaken/reverify/deprecate
	// (2026-08-13, docs/impl/v1/activation.md 状态机): there's no discrete
	// transition to name anymore, only "this link's observed_conditions got
	// cleared/reduced" — written by both Service.Reject (manual) and Study's
	// convergence pruning (automatic), reason/confirmed_by distinguish source.
	ActionPruneCondition = "prune_condition"
	ActionGapFlag        = "gap_flag"
	ActionWikiCandidate  = "wiki_candidate"
	ActionRecompileFlag  = "recompile_flag"
	// Concept evolution actions (docs/impl/v1/concept-evolution.md 步骤 2/3),
	// written through this package's Store since it owns learning_results.
	ActionEntryAddCandidate   = "entry_add_candidate"
	ActionEntryMergeCandidate = "entry_merge_candidate"
	ActionEntryAdd            = "entry_add"
	ActionEntryMerge          = "entry_merge"
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
// owns the shared learning_results table. entry_candidate is concept
// evolution's own audit object (docs/impl/v1/concept-evolution.md).
const (
	ObjectTypeActivationLink = "activation_link"
	ObjectTypeKnowledgeGap   = "knowledge_gap"
	ObjectTypeWikiPage       = "wiki_page"
	ObjectTypeEntryCandidate = "entry_candidate"
)

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
	LinkID             string
	QuestionTerms      string
	SubjectTerms       string
	IntentTerms        []string
	Audience           []string
	ConstraintTerms    []string
	ObservedConditions []ObservedCondition
	Scene              string
	Goal               string
	PointID            string
	Status             string
	AdoptCount         int
	FailCount          int
	LastUsedAt         sql.NullTime
	CreatedFrom        string
	StatusChangedAt    sql.NullTime
	CreatedAt          time.Time
	UpdatedAt          time.Time
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
// MatchedBy is always "exact" (MatchConditionGroups, full four-tuple equality)
// — the round-2 batched model-assisted subject judge that used to produce
// "model" was removed 2026-08-12 along with subject's synonym-based fuzzy
// comparison. The field/constant stay for API/schema stability (retrieval
// evidence, trace, UI already carry a matched_by column).
//
// Tier/Mean/AuditSampled (2026-08-13) are the matched condition's confidence-
// tier verdict for this round (docs/impl/v1/activation.md「置信度分档判定」).
// Subject/Intent/Audience/Constraint are the MATCHED CONDITION'S OWN stored
// quadruple — not the query's — so downstream Trace can call
// Service.RecordOutcome/RecordAuditOutcome against the exact condition that
// served this round, not a re-derivation from the query that could drift
// from it under extraction jitter. On the empty-observed-conditions fallback
// branch (see Matcher.Match), Tier/Mean stay zero-valued and these four
// fields stay empty — there's no concrete condition to attribute to.
type LinkMatch struct {
	Link         ActivationLink
	Score        float64
	MatchedBy    string
	Tier         Tier
	Mean         float64
	AuditSampled bool
	Subject      string
	Intent       string
	Audience     string
	Constraint   string
}

const (
	MatchedByExact = "exact"
)
