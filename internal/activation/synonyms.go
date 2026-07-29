package activation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SubjectSynonym is one row of the subject_synonyms dictionary
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Only the subject dimension of ActivationLink Match is canonicalized
// through this table; intent/audience/constraint keep the exact-match
// semantics defined in matcher.go untouched.
type SubjectSynonym struct {
	SynonymID   string
	DomainID    sql.NullString
	Term        string
	Canonical   string
	Source      string // preset / gap_mined / manual
	Status      string // active / candidate / rejected
	CreatedFrom string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// subject_synonyms.source
const (
	SynonymSourcePreset   = "preset"
	SynonymSourceGapMined = "gap_mined"
	SynonymSourceManual   = "manual"
)

// subject_synonyms.status
const (
	SynonymStatusActive    = "active"
	SynonymStatusCandidate = "candidate"
	SynonymStatusRejected  = "rejected"
)

// learning_results action/object_type for synonym candidates (see
// activation/types.go for the rest of this vocabulary).
const (
	ActionSynonymCandidate   = "synonym_candidate"
	ObjectTypeSubjectSynonym = "subject_synonym"
)

type synonymPair struct {
	term      string
	canonical string
}

// SynonymResolver holds an in-memory, longest-term-first phrase replacement
// table for the subject dimension only. Safe for concurrent use; Load
// replaces the table atomically. It is deliberately not used for
// intent/audience/constraint — those stay exact-match (docs/impl/v1/activation.md
// 步骤 2).
type SynonymResolver struct {
	mu    sync.RWMutex
	pairs []synonymPair
}

func NewSynonymResolver() *SynonymResolver {
	return &SynonymResolver{}
}

// Load replaces the resolver's table from a fresh read of status=active rows.
// Rows where term == canonical are no-ops and dropped. Pairs are sorted by
// term length descending so a longer alias is substituted before a shorter
// one that might be its substring.
func (r *SynonymResolver) Load(rows []SubjectSynonym) {
	pairs := make([]synonymPair, 0, len(rows))
	for _, row := range rows {
		if row.Status != SynonymStatusActive {
			continue
		}
		if row.Term == "" || row.Term == row.Canonical {
			continue
		}
		pairs = append(pairs, synonymPair{term: row.Term, canonical: row.Canonical})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return len(pairs[i].term) > len(pairs[j].term)
	})

	r.mu.Lock()
	r.pairs = pairs
	r.mu.Unlock()
}

// Canonicalize applies phrase-level substring replacement (longest term
// first, single pass over the original text — canonical values are
// pre-resolved at write time, see Study's candidate-direction rule, so no
// chained replacement is needed) to already-text.Normalize()d input. Empty
// input and input with no matching pair are returned unchanged.
func (r *SynonymResolver) Canonicalize(normalizedText string) string {
	if normalizedText == "" {
		return normalizedText
	}
	r.mu.RLock()
	pairs := r.pairs
	r.mu.RUnlock()

	result := normalizedText
	for _, p := range pairs {
		if strings.Contains(result, p.term) {
			result = strings.ReplaceAll(result, p.term, p.canonical)
		}
	}
	return result
}

const synonymColumns = `synonym_id, domain_id, term, canonical, source, status, created_from, created_at, updated_at`

func scanSynonym(row interface{ Scan(...interface{}) error }) (*SubjectSynonym, error) {
	var s SubjectSynonym
	if err := row.Scan(&s.SynonymID, &s.DomainID, &s.Term, &s.Canonical, &s.Source, &s.Status,
		&s.CreatedFrom, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListActiveSynonyms loads every status=active row — the Matcher's
// SynonymResolver refresh source, reloaded together with the link cache
// (docs/impl/v1/activation.md 步骤 2 Subject 同义词归一化).
func (s *Store) ListActiveSynonyms() ([]SubjectSynonym, error) {
	rows, err := s.db.Query(`SELECT `+synonymColumns+` FROM subject_synonyms WHERE status = ?`, SynonymStatusActive)
	if err != nil {
		return nil, fmt.Errorf("activation store: list active synonyms: %w", err)
	}
	defer rows.Close()

	var out []SubjectSynonym
	for rows.Next() {
		syn, err := scanSynonym(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan active synonym: %w", err)
		}
		out = append(out, *syn)
	}
	return out, rows.Err()
}

// ListSynonymsFilter mirrors GET /subject-synonyms query params.
type ListSynonymsFilter struct {
	Status string
	Limit  int
	Offset int
}

func (s *Store) ListSynonyms(f ListSynonymsFilter) ([]SubjectSynonym, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + synonymColumns + ` FROM subject_synonyms WHERE 1 = 1`
	var args []interface{}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list synonyms: %w", err)
	}
	defer rows.Close()

	var out []SubjectSynonym
	for rows.Next() {
		syn, err := scanSynonym(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan synonym: %w", err)
		}
		out = append(out, *syn)
	}
	return out, rows.Err()
}

func (s *Store) GetSynonym(synonymID string) (*SubjectSynonym, error) {
	row := s.db.QueryRow(`SELECT `+synonymColumns+` FROM subject_synonyms WHERE synonym_id = ?`, synonymID)
	syn, err := scanSynonym(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get synonym: %w", err)
	}
	return syn, nil
}

// FindSynonymByTermAnyStatus is Study's dedup check before creating a new
// gap-mined candidate: a term with an existing active/candidate/rejected row
// is never re-proposed (docs/impl/v1/study.md 步骤 2a).
func (s *Store) FindSynonymByTermAnyStatus(term string) (*SubjectSynonym, error) {
	row := s.db.QueryRow(`SELECT `+synonymColumns+` FROM subject_synonyms WHERE term = ? ORDER BY created_at DESC LIMIT 1`, term)
	syn, err := scanSynonym(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: find synonym by term: %w", err)
	}
	return syn, nil
}

// InsertSynonymCandidate creates a status=candidate, source=gap_mined row —
// Study's write path once a subject_synonym_gap pair clears the aggregation
// threshold (docs/impl/v1/study.md 步骤 2a).
func (s *Store) InsertSynonymCandidate(domainID, term, canonical string, createdFrom []string) (*SubjectSynonym, error) {
	createdFromJSON, err := json.Marshal(createdFrom)
	if err != nil {
		return nil, fmt.Errorf("activation store: marshal synonym created_from: %w", err)
	}
	synonymID := uuid.New().String()
	var domainArg interface{}
	if domainID != "" {
		domainArg = domainID
	}
	_, err = s.db.Exec(`INSERT INTO subject_synonyms
		(synonym_id, domain_id, term, canonical, source, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		synonymID, domainArg, term, canonical, SynonymSourceGapMined, SynonymStatusCandidate, string(createdFromJSON))
	if err != nil {
		return nil, fmt.Errorf("activation store: insert synonym candidate: %w", err)
	}
	return s.GetSynonym(synonymID)
}

// InsertActiveSynonym is the auto_promote path (study.synonym_auto_promote=true):
// a candidate that clears the threshold goes straight to active, no pending_confirm.
func (s *Store) InsertActiveSynonym(domainID, term, canonical string, createdFrom []string) (*SubjectSynonym, error) {
	createdFromJSON, err := json.Marshal(createdFrom)
	if err != nil {
		return nil, fmt.Errorf("activation store: marshal synonym created_from: %w", err)
	}
	synonymID := uuid.New().String()
	var domainArg interface{}
	if domainID != "" {
		domainArg = domainID
	}
	_, err = s.db.Exec(`INSERT INTO subject_synonyms
		(synonym_id, domain_id, term, canonical, source, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		synonymID, domainArg, term, canonical, SynonymSourceGapMined, SynonymStatusActive, string(createdFromJSON))
	if err != nil {
		return nil, fmt.Errorf("activation store: insert active synonym: %w", err)
	}
	return s.GetSynonym(synonymID)
}

// UpdateSynonymStatus is the only place a synonym's status changes after
// creation (confirm → active, reject → rejected). Callers are responsible
// for only calling this on candidate rows (see Service.ConfirmSynonym /
// RejectSynonym).
func (s *Store) UpdateSynonymStatus(synonymID, status string) error {
	_, err := s.db.Exec(`UPDATE subject_synonyms SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE synonym_id = ?`,
		status, synonymID)
	if err != nil {
		return fmt.Errorf("activation store: update synonym status: %w", err)
	}
	return nil
}
