package activation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const linkColumns = `link_id, question_terms, subject_terms, intent_terms, audience,
	constraint_terms, observed_conditions, scene, goal, point_id, status, adopt_count, fail_count,
	last_used_at, created_from, status_changed_at, created_at, updated_at`

// encodeTermSet sorts and JSON-encodes an accumulated condition set
// (intent_terms/audience/constraint_terms) — sorting makes the stored form
// deterministic regardless of the caller's map/slice iteration order, which
// both change-detection (conditionEqual in study) and byte-for-byte test
// assertions rely on. nil encodes as "[]", never "null".
func encodeTermSet(values []string) (string, error) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	if sorted == nil {
		sorted = []string{}
	}
	b, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("activation store: encode term set: %w", err)
	}
	return string(b), nil
}

// decodeTermSet parses a stored condition-set column. "" is treated as an
// empty set (defensive — migration 027 rewrites every row to valid JSON, but
// a blank column should degrade to "no values" rather than fail the scan).
func decodeTermSet(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("activation store: decode term set %q: %w", raw, err)
	}
	return values, nil
}

func scanLink(row interface{ Scan(...interface{}) error }) (*ActivationLink, error) {
	var l ActivationLink
	var intentRaw, audienceRaw, constraintRaw, observedRaw string
	err := row.Scan(&l.LinkID, &l.QuestionTerms, &l.SubjectTerms, &intentRaw, &audienceRaw,
		&constraintRaw, &observedRaw, &l.Scene, &l.Goal, &l.PointID, &l.Status, &l.AdoptCount, &l.FailCount,
		&l.LastUsedAt, &l.CreatedFrom, &l.StatusChangedAt, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if l.IntentTerms, err = decodeTermSet(intentRaw); err != nil {
		return nil, err
	}
	if l.Audience, err = decodeTermSet(audienceRaw); err != nil {
		return nil, err
	}
	if l.ConstraintTerms, err = decodeTermSet(constraintRaw); err != nil {
		return nil, err
	}
	if l.ObservedConditions, err = decodeObservedConditions(observedRaw); err != nil {
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
	applyLegacyProjection(l)
	intentJSON, err := encodeTermSet(l.IntentTerms)
	if err != nil {
		return err
	}
	audienceJSON, err := encodeTermSet(l.Audience)
	if err != nil {
		return err
	}
	constraintJSON, err := encodeTermSet(l.ConstraintTerms)
	if err != nil {
		return err
	}
	observedJSON, err := encodeObservedConditions(l.ObservedConditions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO activation_links
		(link_id, question_terms, subject_terms, intent_terms, audience, constraint_terms,
		 observed_conditions, point_id, status, created_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.LinkID, l.QuestionTerms, l.SubjectTerms, intentJSON, audienceJSON, constraintJSON,
		observedJSON, l.PointID, l.Status, l.CreatedFrom)
	if err != nil {
		return fmt.Errorf("activation store: insert link: %w", err)
	}
	return nil
}

func applyLegacyProjection(l *ActivationLink) {
	if len(l.ObservedConditions) == 0 {
		return
	}
	subj, intent, aud, cons := ProjectLegacyFields(l.ObservedConditions)
	l.SubjectTerms = subj
	l.IntentTerms = intent
	l.Audience = aud
	l.ConstraintTerms = cons
}

// PointUnitInfo resolves a link's target KP into displayable context for the
// detail dialog: the KP content, its owning KU's id/center, and the source
// document title (the product/document a link belongs to lives nowhere else
// in the dialog — same-label links are otherwise indistinguishable). Missing
// point (shouldn't happen — FK) degrades to empty strings.
func (s *Store) PointUnitInfo(pointID string) (pointContent, unitID, unitCenter, sourceTitle string, err error) {
	err = s.db.QueryRow(`
		SELECT kp.content, ku.unit_id, ku.center, s.title
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN sources s ON s.source_id = kp.source_id
		WHERE kp.point_id = ?`, pointID).Scan(&pointContent, &unitID, &unitCenter, &sourceTitle)
	if err == sql.ErrNoRows {
		return "", "", "", "", nil
	}
	if err != nil {
		return "", "", "", "", fmt.Errorf("activation store: point unit info: %w", err)
	}
	return pointContent, unitID, unitCenter, sourceTitle, nil
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

// GetByPointID returns the point's single link (idx_al_point_id is a UNIQUE
// index — at most one row can exist), or nil if none. This is the identity
// check Service.CreateLink uses now that point_id, not (question_terms,
// point_id), is the dedup key (docs/impl/v1/activation.md 数据结构).
func (s *Store) GetByPointID(pointID string) (*ActivationLink, error) {
	row := s.db.QueryRow(`SELECT `+linkColumns+` FROM activation_links WHERE point_id = ?`, pointID)
	l, err := scanLink(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activation store: get by point id: %w", err)
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

// UpdateConditions / ReplaceObservedConditions overwrite a link's observed
// condition groups in place (Study full rebuild). Legacy columns are projected
// from the newest group for old UI.
func (s *Store) UpdateConditions(linkID string, cond LinkCondition) error {
	return s.ReplaceObservedConditions(linkID, cond.EffectiveConditions())
}

// ReplaceObservedConditions writes the full observed_conditions list and
// projects legacy fields.
func (s *Store) ReplaceObservedConditions(linkID string, conds []ObservedCondition) error {
	if conds == nil {
		conds = []ObservedCondition{}
	}
	subj, intent, aud, cons := ProjectLegacyFields(conds)
	intentJSON, err := encodeTermSet(intent)
	if err != nil {
		return err
	}
	audienceJSON, err := encodeTermSet(aud)
	if err != nil {
		return err
	}
	constraintJSON, err := encodeTermSet(cons)
	if err != nil {
		return err
	}
	observedJSON, err := encodeObservedConditions(conds)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE activation_links
		SET subject_terms = ?, intent_terms = ?, audience = ?, constraint_terms = ?,
		    observed_conditions = ?, updated_at = CURRENT_TIMESTAMP
		WHERE link_id = ?`, subj, intentJSON, audienceJSON, constraintJSON, observedJSON, linkID)
	if err != nil {
		return fmt.Errorf("activation store: replace observed conditions: %w", err)
	}
	return nil
}

// AppendObservedCondition merges one quadruple into the link (slow-path enrichment).
func (s *Store) AppendObservedCondition(linkID string, add ObservedCondition, max int) error {
	link, err := s.GetByID(linkID)
	if err != nil {
		return err
	}
	if link == nil {
		return fmt.Errorf("activation store: append: link not found: %s", linkID)
	}
	merged := MergeObservedConditions(link.ObservedConditions, add, max)
	return s.ReplaceObservedConditions(linkID, merged)
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
// still lifecycle=current. Prefer ListMatchableLinksForCurrentKP for Match —
// that also includes candidates so Study can accumulate activation_success
// before promotion.
func (s *Store) ListVerifiedLinksForCurrentKP() ([]ActivationLink, error) {
	return s.listLinksForCurrentKP(StatusVerified)
}

// ListMatchableLinksForCurrentKP loads verified + candidate links whose
// target KP is lifecycle=current — the Matcher's cache source
// (docs/impl/v1/activation.md 步骤 2 候选加载). Candidates participate in
// Match so Trace can grade activation_success/failure; Retrieval only
// builds the fast path from verified hits.
func (s *Store) ListMatchableLinksForCurrentKP() ([]ActivationLink, error) {
	return s.listLinksForCurrentKP(StatusVerified, StatusCandidate)
}

func (s *Store) listLinksForCurrentKP(statuses ...string) ([]ActivationLink, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(statuses)+1)
	for i, st := range statuses {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, st)
	}
	args = append(args, "current")

	rows, err := s.db.Query(`SELECT `+linkColumnsPrefixed("al")+`
		FROM activation_links al
		JOIN knowledge_points kp ON kp.point_id = al.point_id
		WHERE al.status IN (`+placeholders+`) AND kp.lifecycle = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list matchable links: %w", err)
	}
	defer rows.Close()

	var links []ActivationLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan matchable link: %w", err)
		}
		links = append(links, *l)
	}
	return links, rows.Err()
}

func linkColumnsPrefixed(alias string) string {
	cols := []string{"link_id", "question_terms", "subject_terms", "intent_terms", "audience",
		"constraint_terms", "observed_conditions", "scene", "goal", "point_id", "status", "adopt_count", "fail_count",
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
	// PointIDs, when non-empty, fetches every link for a known bounded set
	// of points in one call — the 知识地图 concept-page modal's per-KP link
	// badges need this (up to hundreds of KPs per concept), where N calls
	// with PointID would be prohibitive. Mutually exclusive with PointID in
	// practice (both may be set, both apply — AND — but callers don't do
	// that). When set and Limit is 0, defaults to a bulk-sized limit instead
	// of ListLinks' normal browse-page default (see below).
	PointIDs []string
	Limit    int
	Offset   int
}

func buildPointIDPlaceholders(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *Store) ListLinks(f ListLinksFilter) ([]ActivationLinkListRow, error) {
	limit := f.Limit
	if limit <= 0 {
		if len(f.PointIDs) > 0 {
			limit = 5000
		} else {
			limit = 50
		}
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
	if len(f.PointIDs) > 0 {
		ph, phArgs := buildPointIDPlaceholders(f.PointIDs)
		query += ` AND al.point_id IN (` + ph + `)`
		args = append(args, phArgs...)
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
		var intentRaw, audienceRaw, constraintRaw, observedRaw string
		err := rows.Scan(&r.LinkID, &r.QuestionTerms, &r.SubjectTerms, &intentRaw, &audienceRaw,
			&constraintRaw, &observedRaw, &r.Scene, &r.Goal, &r.PointID, &r.Status, &r.AdoptCount, &r.FailCount,
			&r.LastUsedAt, &r.CreatedFrom, &r.StatusChangedAt, &r.CreatedAt, &r.UpdatedAt,
			&r.PointSummary, &r.UnitCenter)
		if err != nil {
			return nil, fmt.Errorf("activation store: scan list row: %w", err)
		}
		if r.IntentTerms, err = decodeTermSet(intentRaw); err != nil {
			return nil, err
		}
		if r.Audience, err = decodeTermSet(audienceRaw); err != nil {
			return nil, err
		}
		if r.ConstraintTerms, err = decodeTermSet(constraintRaw); err != nil {
			return nil, err
		}
		if r.ObservedConditions, err = decodeObservedConditions(observedRaw); err != nil {
			return nil, err
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

// LinkQuestion is one original question associated with an ActivationLink
// (docs/superpowers/specs/2026-07-22-activation-link-questions-ui-design.md).
type LinkQuestion struct {
	Question         string `json:"question"`
	TraceID          string `json:"trace_id"`
	CreatedAt        string `json:"created_at"`
	PathType         string `json:"path_type,omitempty"`
	RetrievalQuality string `json:"retrieval_quality,omitempty"`
}

// ListMatchedQuestions returns traces whose activation_link_ids contain linkID
// (candidate signal hits and verified path hits).
func (s *Store) ListMatchedQuestions(linkID string) ([]LinkQuestion, error) {
	rows, err := s.db.Query(`
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t, json_each(t.activation_link_ids) AS j
		WHERE j.value = ?
		ORDER BY t.created_at ASC`, linkID)
	if err != nil {
		return nil, fmt.Errorf("activation store: list matched questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

// ListCreatedFromQuestions returns create-time fuel questions for
// learning_event IDs stored in activation_links.created_from.
func (s *Store) ListCreatedFromQuestions(eventIDs []string) ([]LinkQuestion, error) {
	if len(eventIDs) == 0 {
		return []LinkQuestion{}, nil
	}
	placeholders := make([]string, len(eventIDs))
	args := make([]interface{}, len(eventIDs))
	for i, id := range eventIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t
		WHERE t.trace_id IN (
			SELECT DISTINCT le.trace_id FROM learning_events le
			WHERE le.event_id IN (` + strings.Join(placeholders, ",") + `)
		)
		ORDER BY t.created_at ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("activation store: list created_from questions: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

// ListConfidentQuestionsForPointBefore returns confident traces that cited
// pointID and occurred at or before before (link creation time). Covers
// legacy cooccurrence creates whose created_from held link_candidate IDs
// instead of learning_event IDs.
func (s *Store) ListConfidentQuestionsForPointBefore(pointID string, before time.Time) ([]LinkQuestion, error) {
	rows, err := s.db.Query(`
		SELECT t.trace_id, t.question, t.created_at, t.path_type, t.retrieval_quality
		FROM traces t, json_each(t.direct_point_ids) AS pid
		WHERE t.retrieval_quality = 'confident'
		  AND pid.value = ?
		  AND datetime(t.created_at) <= datetime(?)
		ORDER BY t.created_at ASC`, pointID, before)
	if err != nil {
		return nil, fmt.Errorf("activation store: list confident questions for point: %w", err)
	}
	defer rows.Close()
	return scanLinkQuestions(rows)
}

func scanLinkQuestions(rows *sql.Rows) ([]LinkQuestion, error) {
	out := make([]LinkQuestion, 0)
	for rows.Next() {
		var q LinkQuestion
		var createdAt time.Time
		if err := rows.Scan(&q.TraceID, &q.Question, &createdAt, &q.PathType, &q.RetrievalQuality); err != nil {
			return nil, fmt.Errorf("activation store: scan link question: %w", err)
		}
		q.CreatedAt = createdAt.Format("2006-01-02T15:04:05Z07:00")
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
