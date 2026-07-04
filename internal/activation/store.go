package activation

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const linkColumns = `link_id, question_terms, subject_terms, intent_terms, audience,
	constraint_terms, scene, goal, point_id, status, adopt_count, fail_count,
	last_used_at, created_from, status_changed_at, created_at, updated_at`

func scanLink(row interface{ Scan(...interface{}) error }) (*ActivationLink, error) {
	var l ActivationLink
	err := row.Scan(&l.LinkID, &l.QuestionTerms, &l.SubjectTerms, &l.IntentTerms, &l.Audience,
		&l.ConstraintTerms, &l.Scene, &l.Goal, &l.PointID, &l.Status, &l.AdoptCount, &l.FailCount,
		&l.LastUsedAt, &l.CreatedFrom, &l.StatusChangedAt, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// InsertLink inserts a new candidate link. Callers must have already checked
// for an existing (question_terms, point_id) row — see Service.CreateLink for
// the idempotency/deprecated-reject logic that belongs in front of this.
func (s *Store) InsertLink(l *ActivationLink) error {
	if l.LinkID == "" {
		l.LinkID = uuid.New().String()
	}
	if l.Status == "" {
		l.Status = StatusCandidate
	}
	if l.CreatedFrom == "" {
		l.CreatedFrom = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO activation_links
		(link_id, question_terms, subject_terms, intent_terms, audience, constraint_terms,
		 point_id, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.LinkID, l.QuestionTerms, l.SubjectTerms, l.IntentTerms, l.Audience, l.ConstraintTerms,
		l.PointID, l.Status, l.CreatedFrom)
	if err != nil {
		return fmt.Errorf("activation store: insert link: %w", err)
	}
	return nil
}

func (s *Store) GetByID(linkID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE link_id = ?`, linkID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by id: %w", err)
	}
	return l, nil
}

func (s *Store) GetByQuestionAndPoint(questionTerms, pointID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE question_terms = ? AND point_id = ?`,
		questionTerms, pointID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by question/point: %w", err)
	}
	return l, nil
}

// UpdateStatus is the only place status_changed_at / updated_at are written
// for a transition. Callers must have already validated the transition is
// legal (see Service.TransitionLink).
func (s *Store) UpdateStatus(linkID, status string) error {
	_, err := s.db.Exec(`UPDATE activation_links
		SET status = ?, status_changed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, status, linkID)
	if err != nil {
		return fmt.Errorf("activation store: update status: %w", err)
	}
	return nil
}

func (s *Store) UpdateStats(linkID string, adoptDelta, failDelta int) error {
	_, err := s.db.Exec(`UPDATE activation_links
		SET adopt_count = adopt_count + ?, fail_count = fail_count + ?, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, adoptDelta, failDelta, linkID)
	if err != nil {
		return fmt.Errorf("activation store: update stats: %w", err)
	}
	return nil
}

func (s *Store) TouchLastUsed(linkIDs []string) error {
	if len(linkIDs) == 0 {
		return nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(linkIDs))
	for i, id := range linkIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	_, err := s.db.Exec(fmt.Sprintf(
		`UPDATE activation_links SET last_used_at = CURRENT_TIMESTAMP WHERE link_id IN (%s)`, placeholders),
		args...)
	if err != nil {
		return fmt.Errorf("activation store: touch last used: %w", err)
	}
	return nil
}

// ListVerifiedLinksForCurrentKP loads every verified link whose target KP is
// still lifecycle=current — the Matcher's cache source
// (docs/impl/v1/activation.md 步骤 2 候选加载).
func (s *Store) ListVerifiedLinksForCurrentKP() ([]ActivationLink, error) {
	rows, err := s.db.Query(`SELECT `+linkColumnsPrefixed("al")+`
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id
		WHERE al.status = ? AND kp.lifecycle = 'current'`, StatusVerified)
	if err != nil {
		return nil, fmt.Errorf("activation store: list verified links: %w", err)
	}
	defer rows.Close()

	var links []ActivationLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan verified link: %w", err)
		}
		links = append(links, *l)
	}
	return links, rows.Err()
}

func linkColumnsPrefixed(alias string) string {
	cols := []string{"link_id", "question_terms", "subject_terms", "intent_terms", "audience",
		"constraint_terms", "scene", "goal", "point_id", "status", "adopt_count", "fail_count",
		"last_used_at", "created_from", "status_changed_at", "created_at", "updated_at"}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

// ListLinksFilter mirrors GET /activation-links query params
// (docs/impl/v1/activation.md 步骤 3).
type ListLinksFilter struct {
	Status  string
	PointID string
	Limit   int
	Offset  int
}

func (s *Store) ListLinks(f ListLinksFilter) ([]ActivationLinkListRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT ` + linkColumnsPrefixed("al") + `, kp.content AS point_summary, ku.center AS unit_center
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE 1 = 1`
	var args []interface{}
	if f.Status != "" {
		query += ` AND al.status = ?`
		args = append(args, f.Status)
	}
	if f.PointID != "" {
		query += ` AND al.point_id = ?`
		args = append(args, f.PointID)
	}
	query += ` ORDER BY al.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list links: %w", err)
	}
	defer rows.Close()

	var results []ActivationLinkListRow
	for rows.Next() {
		var r ActivationLinkListRow
		err := rows.Scan(&r.LinkID, &r.QuestionTerms, &r.SubjectTerms, &r.IntentTerms, &r.Audience,
			&r.ConstraintTerms, &r.Scene, &r.Goal, &r.PointID, &r.Status, &r.AdoptCount, &r.FailCount,
			&r.LastUsedAt, &r.CreatedFrom, &r.StatusChangedAt, &r.CreatedAt, &r.UpdatedAt,
			&r.PointSummary, &r.UnitCenter)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan list row: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) InsertLearningResult(lr *LearningResult) error {
	if lr.ResultID == "" {
		lr.ResultID = uuid.New().String()
	}
	if lr.Status == "" {
		lr.Status = ResultApplied
	}
	if lr.EventIDs == "" {
		lr.EventIDs = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status, confirmed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		lr.ResultID, lr.Action, lr.ObjectType, lr.ObjectID, lr.Reason, lr.EventIDs, lr.Status, lr.ConfirmedBy)
	if err != nil {
		return fmt.Errorf("activation store: insert learning result: %w", err)
	}
	return nil
}

// ApplyTransition atomically updates a link's status and records the
// learning_results row that justifies it. Callers (Service.TransitionLink)
// must have already validated the transition is legal.
func (s *Store) ApplyTransition(linkID, to, action, reason, eventIDsJSON string) (*ActivationLink, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("activation store: apply transition: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE activation_links
		SET status = ?, status_changed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, to, linkID); err != nil {
		return nil, fmt.Errorf("activation store: apply transition: update status: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), action, ObjectTypeActivationLink, linkID, reason, eventIDsJSON, ResultApplied); err != nil {
		return nil, fmt.Errorf("activation store: apply transition: insert learning result: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("activation store: apply transition: commit: %w", err)
	}

	return s.GetByID(linkID)
}

func (s *Store) ListLearningResultsByObject(objectType, objectID string) ([]LearningResult, error) {
	rows, err := s.db.Query(`SELECT result_id, action, object_type, object_id, reason, event_ids,
		status, confirmed_by, created_at, updated_at
		FROM learning_results WHERE object_type = ? AND object_id = ? ORDER BY created_at ASC`,
		objectType, objectID)
	if err != nil {
		return nil, fmt.Errorf("activation store: list learning results: %w", err)
	}
	defer rows.Close()

	var results []LearningResult
	for rows.Next() {
		var lr LearningResult
		if err := rows.Scan(&lr.ResultID, &lr.Action, &lr.ObjectType, &lr.ObjectID, &lr.Reason,
			&lr.EventIDs, &lr.Status, &lr.ConfirmedBy, &lr.CreatedAt, &lr.UpdatedAt); err != nil {
			return nil, fmt.Errorf("activation store: scan learning result: %w", err)
		}
		results = append(results, lr)
	}
	return results, rows.Err()
}

// FindPendingPromote finds the most recent pending_confirm/promote learning
// result for a link — the row Study will have written when a candidate met
// the promotion threshold (docs/impl/v1/activation.md 步骤 3 确认/驳回与 Study 的关系).
func (s *Store) FindPendingPromote(linkID string) (*LearningResult, error) {
	row := s.db.QueryRow(`SELECT result_id, action, object_type, object_id, reason, event_ids,
		status, confirmed_by, created_at, updated_at
		FROM learning_results
		WHERE object_type = ? AND object_id = ? AND action = ? AND status = ?
		ORDER BY created_at DESC LIMIT 1`,
		ObjectTypeActivationLink, linkID, ActionPromote, ResultPendingConfirm)

	var lr LearningResult
	err := row.Scan(&lr.ResultID, &lr.Action, &lr.ObjectType, &lr.ObjectID, &lr.Reason,
		&lr.EventIDs, &lr.Status, &lr.ConfirmedBy, &lr.CreatedAt, &lr.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: find pending promote: %w", err)
	}
	return &lr, nil
}

func (s *Store) ResolvePending(resultID, status, confirmedBy string) error {
	_, err := s.db.Exec(`UPDATE learning_results
		SET status = ?, confirmed_by = ?, updated_at = CURRENT_TIMESTAMP
		WHERE result_id = ?`, status, confirmedBy, resultID)
	if err != nil {
		return fmt.Errorf("activation store: resolve pending: %w", err)
	}
	return nil
}
