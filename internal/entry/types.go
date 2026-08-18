// Package entry implements entry (词条) evolution (docs/impl/v1/concept-evolution.md,
// V1 顺序第 10 位): Study observes activation_gap(entry_gap) events and
// cross-concept adoption co-occurrence, forms add/merge candidates, and
// executes them in a single transaction. kind=merge candidates (restructuring
// existing concepts) still require human confirmation; kind=add candidates
// (new concept/fact creation) are auto-confirmed by default as of 2026-08-14
// (Config.AutoConfirmAdd, docs/design/concept-evolution.md "2026-08-14 改判").
package entry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	KindAdd   = "add"
	KindMerge = "merge"
)

// Concept kind classification (docs/impl/v1/kpn.md 步骤 3 "类型标注",
// 2026-08-04 新增，最小可行版本) — orthogonal to Kind (add/merge/split)
// above, hence the distinct EntryKind* naming to avoid confusion. concept:
// 底层理论/原理/规则，跨具体实现成立；fact：具体实现/技术/产品实例，是理论
// 落地后的对象。
const (
	EntryKindConcept = "concept"
	EntryKindFact    = "fact"
)

// ValidateEntryKind normalizes/validates a candidate or concept kind
// value: empty defaults to EntryKindConcept (存量/未分类默认按 concept 处理,
// per 043_entry_kind.sql's column default), anything other than the two
// allowed values is rejected.
func ValidateEntryKind(kind string) (string, error) {
	switch kind {
	case "":
		return EntryKindConcept, nil
	case EntryKindConcept, EntryKindFact:
		return kind, nil
	default:
		return "", fmt.Errorf("concept: invalid kind %q, must be %q or %q", kind, EntryKindConcept, EntryKindFact)
	}
}

const (
	StatusPendingConfirm = "pending_confirm"
	StatusApplied        = "applied"
	StatusRejected       = "rejected"
	StatusExpired        = "expired"
)

// CandidateRow is a entry_candidates row, JSON columns kept as raw strings
// (parsed by callers that need the structured view — docs/impl/v1/concept-evolution.md
// 数据结构).
type CandidateRow struct {
	CandidateID   string
	Kind          string
	EntryKind   string // concept / fact classification (043_entry_kind.sql entry_candidates.entry_kind) — distinct from Kind (add/merge)
	DomainID      sql.NullString
	SuggestedName sql.NullString
	MergeFrom     string // JSON array of entry_id
	PointIDs      string // JSON array of point_id
	Evidence      string // JSON object
	EventIDs      string // JSON array of learning_event event_id
	Status        string
	LastSignalAt  time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// ResolvedEntryID/CreatedNewEntry are set on confirm (kind=add only)
	// and record whether this confirm created a brand-new concept row —
	// the only case RestoreApplied can safely undo (assign-to-existing and
	// merge confirms touch a concept this candidate didn't create).
	ResolvedEntryID sql.NullString
	CreatedNewEntry bool
	// KPNRelationIDs is the JSON array of knowledge_point_relations.relation_id
	// rows RematchPoints created for this candidate's confirm (kind=add only;
	// '[]' otherwise) — RestoreAppliedNewEntry deletes exactly these on
	// restore.
	KPNRelationIDs string
	// ParentEntryID is the concept entry_id a fact candidate was classified
	// under at generation time (kpn.md 步骤 3 fact 新建 — kpn_orphan_fact_
	// match.md's matched_concept_id), persisted verbatim through confirm onto
	// entries.parent_entry_id (docs/impl/v1/fact-entry-parent-concept-task-
	// brief.md). NULL for concept candidates and any candidate that didn't
	// come from that fact-grouping path.
	ParentEntryID sql.NullString
}

// AddEvidence is the evidence JSON for kind=add candidates.
type AddEvidence struct {
	EventCount    int     `json:"event_count"`
	DistinctCount int     `json:"distinct_question_count"`
	Overlap       float64 `json:"overlap"`
}

// ContentDrivenEvidence is the evidence JSON for kind=add candidates
// produced by KPN's cross-Source matching (docs/impl/v1/kpn.md 步骤 3),
// as opposed to this module's own usage-driven AddEvidence — Origin is
// always "content_driven" and lets the UI/report distinguish the two
// sources sharing the same entry_candidates table.
type ContentDrivenEvidence struct {
	Origin      string   `json:"origin"`
	SourceIDs   []string `json:"source_ids"`
	Description string   `json:"description"`
	// Boundary is kpn_entry_propose.md's suggested_boundary (2026-08-05
	// schema addition) — carried on the pending candidate so confirm-time
	// can write entries.boundary for evolved (LLM-proposed) entries the
	// same way preset entries already have one.
	Boundary string `json:"boundary,omitempty"`
	// Entity is the fact cluster's extracted entity name (empty for
	// kind=concept candidates) — kept so a later merge (proposeAddCandidate
	// folding more point_ids into this same pending candidate) can detect a
	// new batch naming the same real-world thing under a different string
	// and record it as an alias instead of silently ignoring the variant.
	Entity string `json:"entity,omitempty"`
	// Aliases accumulates alternate entity names observed across merges
	// (2026-08-05, docs/impl/v1/kpn.md 步骤 3) — written to entries.aliases
	// at confirm time. Never includes Entity itself.
	Aliases []string `json:"aliases,omitempty"`
}

// MergeEvidence is the evidence JSON for kind=merge candidates. OverlapRatio
// is a trace-occurrence overlap coefficient — cooccur_count / |traces citing
// A or B| — not a KP-set Jaccard: a KP belongs to exactly one concept via
// knowledge_units.entry_id, so concept A's and B's adopted-KP sets are
// structurally disjoint and a KP-set Jaccard would always be ~0. This
// measures instead how often the two entries are needed together relative
// to how often either is needed at all.
type MergeEvidence struct {
	CooccurCount int      `json:"cooccur_count"`
	OverlapRatio float64  `json:"overlap_ratio"`
	TotalTracesA int      `json:"total_traces_a"`
	TotalTracesB int      `json:"total_traces_b"`
	PointIDsA    []string `json:"point_ids_a"`
	PointIDsB    []string `json:"point_ids_b"`
}

// ScanSummary is what one Study cycle's concept-candidate scan produced,
// folded into the study report's entry_candidates section
// (docs/impl/v1/concept-evolution.md 步骤 5).
type ScanSummary struct {
	AddCreated           int
	AddUpdated           int
	MergeCreated         int
	MergeUpdated         int
	Expired              int
	EntryGapEventCount int // windowed activation_gap(entry_gap) events observed this cycle
}

// CandidateView is CandidateRow with its JSON columns parsed, for API/report
// display (docs/impl/v1/concept-evolution.md 步骤 3/5).
type CandidateView struct {
	CandidateID   string          `json:"candidate_id"`
	Kind          string          `json:"kind"`
	EntryKind   string          `json:"entry_kind"`
	DomainID      string          `json:"domain_id,omitempty"`
	SuggestedName string          `json:"suggested_name,omitempty"`
	MergeFrom     []string        `json:"merge_from,omitempty"`
	PointIDs      []string        `json:"point_ids"`
	Evidence      json.RawMessage `json:"evidence"`
	EventIDs      []string        `json:"event_ids,omitempty"`
	Status        string          `json:"status"`
	LastSignalAt  time.Time       `json:"last_signal_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	// Restorable: true when this is an applied kind=add candidate that
	// created a brand-new concept — the confirm dialog's "恢复到待确认"
	// button only shows for these (docs/impl/v1/concept-evolution.md has no
	// restore design; scope intentionally limited to the new-concept path).
	Restorable bool `json:"restorable"`
}

func toView(c CandidateRow) CandidateView {
	v := CandidateView{
		CandidateID:  c.CandidateID,
		Kind:         c.Kind,
		EntryKind:  c.EntryKind,
		Status:       c.Status,
		LastSignalAt: c.LastSignalAt,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
		Evidence:     json.RawMessage(c.Evidence),
	}
	if c.DomainID.Valid {
		v.DomainID = c.DomainID.String
	}
	if c.SuggestedName.Valid {
		v.SuggestedName = c.SuggestedName.String
	}
	json.Unmarshal([]byte(c.MergeFrom), &v.MergeFrom)
	json.Unmarshal([]byte(c.PointIDs), &v.PointIDs)
	json.Unmarshal([]byte(c.EventIDs), &v.EventIDs)
	v.Restorable = c.Status == StatusApplied && c.Kind == KindAdd && c.CreatedNewEntry
	return v
}

// ConfirmAddRequest is POST /entries/candidates/:id/confirm's body for
// kind=add: both fields may override the candidate's own suggestion, and
// DomainID is required when the candidate's own domain_id is NULL
// (docs/impl/v1/concept-evolution.md 步骤 3).
type ConfirmAddRequest struct {
	SuggestedName string `json:"suggested_name"`
	DomainID      string `json:"domain_id"`
	// Description overrides the candidate's own evidence.description (if
	// any) as the new concept row's description — editable in the confirm
	// dialog alongside the name. Only used on the new-concept path; ignored
	// when EntryID is set (归入已有概念 assigns to an existing concept's
	// own description, which is edited separately via the concept edit view).
	Description string `json:"description"`
	// EntryKind overrides the candidate's own EntryKind (concept/fact,
	// docs/impl/v1/kpn.md 步骤 3 "类型标注") when the human edits it at confirm
	// time — mirrors SuggestedName/Description above. Empty means "use the
	// candidate's own suggestion, unchanged". Only used on the new-concept
	// path; ignored when EntryID is set (归入已有概念 keeps the existing
	// concept's own kind).
	EntryKind string `json:"entry_kind"`
	// Boundary overrides the candidate's own evidence.boundary (kpn_entry_
	// propose.md's suggested_boundary, 2026-08-05) — mirrors Description.
	// Empty means "use whatever the LLM/merge pipeline produced, unchanged".
	// Only used on the new-concept path.
	Boundary string `json:"boundary"`
	// EntryID, when set, skips creating a new concept and instead assigns
	// the candidate's point_ids to this already-existing entry_id
	// (docs/impl/v1/kpn.md 步骤 6 "归入已有概念") — mutually exclusive with
	// SuggestedName/DomainID, which are ignored when EntryID is set.
	EntryID string `json:"entry_id"`
	// PointIDs, when non-nil, replaces the candidate's own point_ids
	// wholesale (add/remove KPs via the confirm dialog's picker). Applies to
	// both the new-concept and "归入已有概念" execution paths. Nil (vs. an
	// explicit empty array) means "use the candidate's own suggestion,
	// unchanged" — distinguished via JSON decode, see the handler.
	PointIDs []string `json:"point_ids"`
}

// ConfirmMergeRequest is POST /entries/candidates/:id/confirm's body for
// kind=merge: Target must be one of the candidate's merge_from entry_ids.
type ConfirmMergeRequest struct {
	Target string `json:"target"`
}

// ConfirmResult is the confirm execution's audit summary
// (docs/impl/v1/concept-evolution.md 步骤 3 reason 摘要).
type ConfirmResult struct {
	Candidate    CandidateRow
	EntryID    string // kind=add: the newly created entry_id
	MigratedKUs  int
	FlaggedPages int
}
