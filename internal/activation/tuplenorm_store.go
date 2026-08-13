package activation

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// QuestionTupleNorm is one row of question_tuple_norms (migration 049) —
// a canonical四元组 that later, wording-jittered extractions normalize onto.
// See TupleNormalizer for the four-tier lookup that produces/consumes rows
// of this shape.
type QuestionTupleNorm struct {
	NormID         string
	DomainID       string
	Subject        string
	Intent         string
	Audience       string
	ConstraintText string
	Vector         sql.NullString
	LastHitAt      time.Time
	CreatedAt      time.Time
}

const tupleNormColumns = `norm_id, domain_id, subject, intent, audience, constraint_text, vector, last_hit_at, created_at`

func scanTupleNorm(row interface{ Scan(...interface{}) error }) (*QuestionTupleNorm, error) {
	var n QuestionTupleNorm
	if err := row.Scan(&n.NormID, &n.DomainID, &n.Subject, &n.Intent, &n.Audience, &n.ConstraintText,
		&n.Vector, &n.LastHitAt, &n.CreatedAt); err != nil {
		return nil, err
	}
	return &n, nil
}

// FindExactMatch is Tier 1 of TupleNormalizer.Normalize: an exact string
// match on all four normalized fields, scoped to any of domainIDs.
func (s *Store) FindExactMatch(domainIDs []string, subject, intent, audience, constraint string) (*QuestionTupleNorm, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(domainIDs))
	args := make([]interface{}, 0, len(domainIDs)+4)
	for i, d := range domainIDs {
		placeholders[i] = "?"
		args = append(args, d)
	}
	args = append(args, subject, intent, audience, constraint)

	query := fmt.Sprintf(`SELECT %s FROM question_tuple_norms
		WHERE domain_id IN (%s) AND subject = ? AND intent = ? AND audience = ? AND constraint_text = ?
		ORDER BY last_hit_at DESC LIMIT 1`, tupleNormColumns, strings.Join(placeholders, ","))
	row := s.db.QueryRow(query, args...)
	n, err := scanTupleNorm(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: find exact tuple norm match: %w", err)
	}
	return n, nil
}

// ListCandidatesByDomain returns up to limit rows across domainIDs, most
// recently hit first — the candidate pool for Tier 2 (local similarity) and
// Tier 2.5 (vector). limit<=0 defaults to 200.
func (s *Store) ListCandidatesByDomain(domainIDs []string, limit int) ([]QuestionTupleNorm, error) {
	if len(domainIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	placeholders := make([]string, len(domainIDs))
	args := make([]interface{}, 0, len(domainIDs)+1)
	for i, d := range domainIDs {
		placeholders[i] = "?"
		args = append(args, d)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT %s FROM question_tuple_norms
		WHERE domain_id IN (%s) ORDER BY last_hit_at DESC LIMIT ?`, tupleNormColumns, strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list tuple norm candidates: %w", err)
	}
	defer rows.Close()

	var out []QuestionTupleNorm
	for rows.Next() {
		n, err := scanTupleNorm(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan tuple norm candidate: %w", err)
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// Insert writes a new question_tuple_norms row. Callers set NormID to a
// fresh uuid when empty.
func (s *Store) InsertTupleNorm(n *QuestionTupleNorm) error {
	if n.NormID == "" {
		n.NormID = uuid.NewString()
	}
	now := time.Now().UTC()
	if n.LastHitAt.IsZero() {
		n.LastHitAt = now
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	_, err := s.db.Exec(`INSERT INTO question_tuple_norms
		(norm_id, domain_id, subject, intent, audience, constraint_text, vector, last_hit_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.NormID, n.DomainID, n.Subject, n.Intent, n.Audience, n.ConstraintText, n.Vector, n.LastHitAt, n.CreatedAt)
	if err != nil {
		return fmt.Errorf("activation store: insert tuple norm: %w", err)
	}
	return nil
}

// TouchLastHit bumps last_hit_at to now for the given row — called whenever
// a norm is the matched target of any tier's hit.
func (s *Store) TouchLastHit(normID string) error {
	_, err := s.db.Exec(`UPDATE question_tuple_norms SET last_hit_at = ? WHERE norm_id = ?`, time.Now().UTC(), normID)
	if err != nil {
		return fmt.Errorf("activation store: touch tuple norm last_hit_at: %w", err)
	}
	return nil
}

// DeleteIdleOlderThan removes rows whose last_hit_at is older than days ago.
// Returns the count deleted, for Study's periodic housekeeping loop
// (docs/impl/v1/study.md 步骤 4 同款 idle 清理惯例).
func (s *Store) DeleteIdleOlderThan(days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	res, err := s.db.Exec(`DELETE FROM question_tuple_norms WHERE last_hit_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("activation store: delete idle tuple norms: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("activation store: delete idle tuple norms rows affected: %w", err)
	}
	return int(n), nil
}
