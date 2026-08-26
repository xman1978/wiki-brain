package activation

import (
	"database/sql"
	"fmt"
	"time"
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
	SynonymSourcePreset = "preset"
	SynonymSourceManual = "manual"
)

// subject_synonyms.status
const (
	SynonymStatusActive    = "active"
	SynonymStatusCandidate = "candidate"
	SynonymStatusRejected  = "rejected"
)

const synonymColumns = `synonym_id, domain_id, term, canonical, source, status, created_from, created_at, updated_at`

func scanSynonym(row interface{ Scan(...interface{}) error }) (*SubjectSynonym, error) {
	var s SubjectSynonym
	if err := row.Scan(&s.SynonymID, &s.DomainID, &s.Term, &s.Canonical, &s.Source, &s.Status,
		&s.CreatedFrom, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
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
