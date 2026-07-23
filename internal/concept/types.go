// Package concept implements concept evolution (docs/impl/v1/concept-evolution.md,
// V1 顺序第 10 位): Study observes activation_gap(concept_gap) events and
// cross-concept adoption co-occurrence, forms add/merge candidates, and
// executes them — always under human confirmation — in a single transaction.
package concept

import (
	"database/sql"
	"encoding/json"
	"time"
)

const (
	KindAdd   = "add"
	KindMerge = "merge"
)

const (
	StatusPendingConfirm = "pending_confirm"
	StatusApplied        = "applied"
	StatusRejected       = "rejected"
	StatusExpired        = "expired"
)

// CandidateRow is a concept_candidates row, JSON columns kept as raw strings
// (parsed by callers that need the structured view — docs/impl/v1/concept-evolution.md
// 数据结构).
type CandidateRow struct {
	CandidateID   string
	Kind          string
	DomainID      sql.NullString
	SuggestedName sql.NullString
	MergeFrom     string // JSON array of concept_id
	PointIDs      string // JSON array of point_id
	Evidence      string // JSON object
	EventIDs      string // JSON array of learning_event event_id
	Status        string
	LastSignalAt  time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// ResolvedConceptID/CreatedNewConcept are set on confirm (kind=add only)
	// and record whether this confirm created a brand-new concept row —
	// the only case RestoreApplied can safely undo (assign-to-existing and
	// merge confirms touch a concept this candidate didn't create).
	ResolvedConceptID sql.NullString
	CreatedNewConcept bool
	// KPNRelationIDs is the JSON array of knowledge_point_relations.relation_id
	// rows RematchPoints created for this candidate's confirm (kind=add only;
	// '[]' otherwise) — RestoreAppliedNewConcept deletes exactly these on
	// restore.
	KPNRelationIDs string
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
// sources sharing the same concept_candidates table.
type ContentDrivenEvidence struct {
	Origin      string   `json:"origin"`
	SourceIDs   []string `json:"source_ids"`
	Description string   `json:"description"`
}

// MergeEvidence is the evidence JSON for kind=merge candidates. OverlapRatio
// is a trace-occurrence overlap coefficient — cooccur_count / |traces citing
// A or B| — not a KP-set Jaccard: a KP belongs to exactly one concept via
// knowledge_units.concept_id, so concept A's and B's adopted-KP sets are
// structurally disjoint and a KP-set Jaccard would always be ~0. This
// measures instead how often the two concepts are needed together relative
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
// folded into the study report's concept_candidates section
// (docs/impl/v1/concept-evolution.md 步骤 5).
type ScanSummary struct {
	AddCreated           int
	AddUpdated           int
	MergeCreated         int
	MergeUpdated         int
	Expired              int
	ConceptGapEventCount int // windowed activation_gap(concept_gap) events observed this cycle
}

// CandidateView is CandidateRow with its JSON columns parsed, for API/report
// display (docs/impl/v1/concept-evolution.md 步骤 3/5).
type CandidateView struct {
	CandidateID   string          `json:"candidate_id"`
	Kind          string          `json:"kind"`
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
	v.Restorable = c.Status == StatusApplied && c.Kind == KindAdd && c.CreatedNewConcept
	return v
}

// ConfirmAddRequest is POST /concepts/candidates/:id/confirm's body for
// kind=add: both fields may override the candidate's own suggestion, and
// DomainID is required when the candidate's own domain_id is NULL
// (docs/impl/v1/concept-evolution.md 步骤 3).
type ConfirmAddRequest struct {
	SuggestedName string `json:"suggested_name"`
	DomainID      string `json:"domain_id"`
	// ConceptID, when set, skips creating a new concept and instead assigns
	// the candidate's point_ids to this already-existing concept_id
	// (docs/impl/v1/kpn.md 步骤 6 "归入已有概念") — mutually exclusive with
	// SuggestedName/DomainID, which are ignored when ConceptID is set.
	ConceptID string `json:"concept_id"`
	// PointIDs, when non-nil, replaces the candidate's own point_ids
	// wholesale (add/remove KPs via the confirm dialog's picker). Applies to
	// both the new-concept and "归入已有概念" execution paths. Nil (vs. an
	// explicit empty array) means "use the candidate's own suggestion,
	// unchanged" — distinguished via JSON decode, see the handler.
	PointIDs []string `json:"point_ids"`
}

// ConfirmMergeRequest is POST /concepts/candidates/:id/confirm's body for
// kind=merge: Target must be one of the candidate's merge_from concept_ids.
type ConfirmMergeRequest struct {
	Target string `json:"target"`
}

// ConfirmResult is the confirm execution's audit summary
// (docs/impl/v1/concept-evolution.md 步骤 3 reason 摘要).
type ConfirmResult struct {
	Candidate    CandidateRow
	ConceptID    string // kind=add: the newly created concept_id
	MigratedKUs  int
	FlaggedPages int
}
