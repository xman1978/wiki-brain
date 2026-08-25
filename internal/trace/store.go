package trace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) SaveTrace(t *Trace) error {
	pointIDsJSON, err := json.Marshal(t.DirectPointIDs)
	if err != nil {
		return fmt.Errorf("trace store: marshal direct_point_ids: %w", err)
	}
	linkIDsJSON, err := json.Marshal(nonNilStrings(t.ActivationLinkIDs))
	if err != nil {
		return fmt.Errorf("trace store: marshal activation_link_ids: %w", err)
	}
	bundleIDsJSON, err := json.Marshal(nonNilStrings(t.ActivationBundleIDs))
	if err != nil {
		return fmt.Errorf("trace store: marshal activation_bundle_ids: %w", err)
	}

	hasFeedback := 0
	if t.HasFeedback {
		hasFeedback = 1
	}

	_, err = s.db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms,
		retrieval_quality, path, path_type, activation_link_ids, activation_bundle_ids, subject, intent, audience, constraint_text,
		direct_point_ids, kpn_cited_count, cited_count, outline_cited_count, cited_rank_sum,
		has_feedback, feedback_type, feedback_content)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TraceID, t.AnswerID, t.Question, t.QuestionHash, t.QuestionTerms,
		t.RetrievalQuality, t.Path, t.PathType, string(linkIDsJSON), string(bundleIDsJSON), t.Subject, t.Intent, t.Audience, t.ConstraintText,
		string(pointIDsJSON), t.KPNCitedCount, t.CitedCount, t.OutlineCitedCount, t.CitedRankSum, hasFeedback,
		nullString(t.FeedbackType), nullString(t.FeedbackContent),
	)
	if err != nil {
		return fmt.Errorf("trace store: insert: %w", err)
	}
	return nil
}

// SaveAuditPlaceholder inserts a minimal answers+traces row pair to satisfy
// learning_events.trace_id's NOT NULL REFERENCES traces(trace_id) FK
// (foreign_keys=ON) for an independent-verification audit trial
// (docs/impl/v1/retrieval.md 步骤 2c / docs/impl/v1/trace.md 步骤 3b). An audit
// trial runs in the background, detached from any specific user-facing
// answer/trace — there's no real trace_id available at the point Retrieval
// triggers it — so this creates a real, minimal, otherwise-inert row pair
// (empty content/path/etc.) purely to hold the FK, tagged path_type="audit"
// so it's identifiable/filterable if ever inspected directly. Returns the new
// trace_id.
func (s *Store) SaveAuditPlaceholder(question, subject, intent, audience, constraint string) (string, error) {
	answerID := uuid.New().String()
	_, err := s.db.Exec(`INSERT INTO answers (answer_id, question, content, has_answer, path, prompt_version, model_name)
		VALUES (?, ?, '', 0, 'audit', 'audit', 'audit')`, answerID, question)
	if err != nil {
		return "", fmt.Errorf("trace store: insert audit placeholder answer: %w", err)
	}

	traceID := uuid.New().String()
	_, err = s.db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms,
		retrieval_quality, path, path_type, activation_link_ids, activation_bundle_ids, subject, intent, audience, constraint_text,
		direct_point_ids, kpn_cited_count, cited_count, outline_cited_count, cited_rank_sum,
		has_feedback, feedback_type, feedback_content)
		VALUES (?, ?, ?, '', '', 'unknown', 'audit', 'audit', '[]', '[]', ?, ?, ?, ?, '[]', 0, 0, 0, 0, 0, NULL, NULL)`,
		traceID, answerID, question, subject, intent, audience, constraint,
	)
	if err != nil {
		return "", fmt.Errorf("trace store: insert audit placeholder trace: %w", err)
	}
	return traceID, nil
}

func (s *Store) GetTrace(traceID string) (*Trace, error) {
	var (
		t               Trace
		pointIDsStr     string
		linkIDsStr      string
		bundleIDsStr    string
		hasFeedbackInt  int
		feedbackType    sql.NullString
		feedbackContent sql.NullString
	)
	err := s.db.QueryRow(`SELECT trace_id, answer_id, question, question_hash, question_terms,
		retrieval_quality, path, path_type, activation_link_ids, activation_bundle_ids, subject, intent, audience, constraint_text,
		direct_point_ids, kpn_cited_count, cited_count, outline_cited_count, cited_rank_sum,
		has_feedback, feedback_type, feedback_content,
		created_at, updated_at
		FROM traces WHERE trace_id = ?`, traceID).
		Scan(&t.TraceID, &t.AnswerID, &t.Question, &t.QuestionHash, &t.QuestionTerms,
			&t.RetrievalQuality, &t.Path, &t.PathType, &linkIDsStr, &bundleIDsStr, &t.Subject, &t.Intent, &t.Audience, &t.ConstraintText,
			&pointIDsStr, &t.KPNCitedCount, &t.CitedCount, &t.OutlineCitedCount, &t.CitedRankSum, &hasFeedbackInt,
			&feedbackType, &feedbackContent, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trace store: get: %w", err)
	}

	if err := json.Unmarshal([]byte(pointIDsStr), &t.DirectPointIDs); err != nil {
		return nil, fmt.Errorf("trace store: unmarshal direct_point_ids: %w", err)
	}
	if err := json.Unmarshal([]byte(linkIDsStr), &t.ActivationLinkIDs); err != nil {
		return nil, fmt.Errorf("trace store: unmarshal activation_link_ids: %w", err)
	}
	if err := json.Unmarshal([]byte(bundleIDsStr), &t.ActivationBundleIDs); err != nil {
		return nil, fmt.Errorf("trace store: unmarshal activation_bundle_ids: %w", err)
	}
	t.HasFeedback = hasFeedbackInt == 1
	t.FeedbackType = feedbackType.String
	t.FeedbackContent = feedbackContent.String
	return &t, nil
}

func (s *Store) ListTraces(quality, answerID, pathType string, limit, offset int) ([]Trace, error) {
	query := `SELECT trace_id, answer_id, question, retrieval_quality, path_type, activation_link_ids, has_feedback, created_at
		FROM traces WHERE 1=1`
	var args []interface{}

	if quality != "" {
		query += ` AND retrieval_quality = ?`
		args = append(args, quality)
	}
	if answerID != "" {
		query += ` AND answer_id = ?`
		args = append(args, answerID)
	}
	if pathType != "" {
		query += ` AND path_type = ?`
		args = append(args, pathType)
	}
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("trace store: list: %w", err)
	}
	defer rows.Close()

	var traces []Trace
	for rows.Next() {
		var t Trace
		var linkIDsStr string
		var hasFeedbackInt int
		if err := rows.Scan(&t.TraceID, &t.AnswerID, &t.Question, &t.RetrievalQuality, &t.PathType, &linkIDsStr, &hasFeedbackInt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("trace store: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(linkIDsStr), &t.ActivationLinkIDs); err != nil {
			return nil, fmt.Errorf("trace store: unmarshal activation_link_ids: %w", err)
		}
		t.HasFeedback = hasFeedbackInt == 1
		traces = append(traces, t)
	}
	return traces, rows.Err()
}

func (s *Store) UpdateFeedback(traceID, feedbackType, feedbackContent string) error {
	_, err := s.db.Exec(`UPDATE traces SET has_feedback = 1, feedback_type = ?, feedback_content = ?, updated_at = CURRENT_TIMESTAMP
		WHERE trace_id = ?`, feedbackType, feedbackContent, traceID)
	if err != nil {
		return fmt.Errorf("trace store: update feedback: %w", err)
	}
	return nil
}

func (s *Store) UpdateCooccurrence(questionHash, questionTerms string, pointIDs []string, quality string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("trace store: begin tx: %w", err)
	}
	defer tx.Rollback()

	for _, pid := range pointIDs {
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM cooccurrence_question_dedup WHERE question_hash = ? AND point_id = ?`,
			questionHash, pid).Scan(&exists)

		if err == nil {
			slog.Debug("trace: duplicate question, skipping cooccurrence update",
				"question_hash", questionHash, "point_id", pid)
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("trace store: check dedup: %w", err)
		}

		_, err = tx.Exec(`INSERT INTO cooccurrence_question_dedup (question_hash, point_id) VALUES (?, ?)`,
			questionHash, pid)
		if err != nil {
			return fmt.Errorf("trace store: insert dedup: %w", err)
		}

		confidentIncr := 0
		if quality == QualityConfident {
			confidentIncr = 1
		}

		coocID := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO question_kp_cooccurrence (cooc_id, question_terms, point_id, hit_count, confident_count, last_seen_at)
			VALUES (?, ?, ?, 1, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(question_terms, point_id) DO UPDATE SET
				hit_count = hit_count + 1,
				confident_count = confident_count + ?,
				last_seen_at = CURRENT_TIMESTAMP`,
			coocID, questionTerms, pid, confidentIncr, confidentIncr)
		if err != nil {
			return fmt.Errorf("trace store: upsert cooccurrence: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) SaveLearningEvent(traceID, eventType, payload string) (string, error) {
	eventID := uuid.New().String()
	_, err := s.db.Exec(`INSERT INTO learning_events (event_id, trace_id, event_type, payload) VALUES (?, ?, ?, ?)`,
		eventID, traceID, eventType, payload)
	if err != nil {
		return "", fmt.Errorf("trace store: insert learning_event: %w", err)
	}
	return eventID, nil
}

// EntryNullRatio computes, for a set of KnowledgePoint IDs, the share
// without a current concept anchor — either the owning KnowledgeUnit has no
// entry_id, or it does but that concept has been merged_into another one
// (docs/impl/v1/concept-evolution.md activation_gap payload 扩展). One join,
// no LLM call. Empty input reports ratio 0 (link_gap by construction, since
// no threshold triggers on a zero ratio).
func (s *Store) EntryNullRatio(pointIDs []string) (float64, error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(pointIDs))
	args := make([]interface{}, len(pointIDs))
	for i, pid := range pointIDs {
		placeholders[i] = "?"
		args[i] = pid
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*),
			SUM(CASE WHEN ku.entry_id IS NULL OR c.merged_into IS NOT NULL THEN 1 ELSE 0 END)
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		LEFT JOIN entries c ON ku.entry_id = c.entry_id
		WHERE kp.point_id IN (%s)`, strings.Join(placeholders, ","))

	var total int
	var nullCount sql.NullInt64
	if err := s.db.QueryRow(query, args...).Scan(&total, &nullCount); err != nil {
		return 0, fmt.Errorf("trace store: concept null ratio: %w", err)
	}
	if total == 0 {
		return 0, nil
	}
	return float64(nullCount.Int64) / float64(total), nil
}

// AmbiguousUnitPoints returns, for each unitID whose knowledge_units currently
// has more than one lifecycle='current' knowledge_point, the full list of
// those points (id + content). Units with 0 or 1 current point are omitted
// entirely — they carry no point-binding ambiguity for resolvePointBinding
// to resolve, so callers skip the LLM call for them (docs/impl/v1/trace.md
// point-binding resolution step).
func (s *Store) AmbiguousUnitPoints(unitIDs []string) (map[string][]PointSummary, error) {
	result := make(map[string][]PointSummary)
	if len(unitIDs) == 0 {
		return result, nil
	}

	seen := make(map[string]bool, len(unitIDs))
	placeholders := make([]string, 0, len(unitIDs))
	args := make([]interface{}, 0, len(unitIDs))
	for _, id := range unitIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return result, nil
	}

	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT point_id, unit_id, content FROM knowledge_points
		 WHERE unit_id IN (%s) AND lifecycle = 'current'
		 ORDER BY unit_id, created_at ASC`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("trace store: ambiguous unit points: %w", err)
	}
	defer rows.Close()

	byUnit := make(map[string][]PointSummary)
	for rows.Next() {
		var p PointSummary
		if err := rows.Scan(&p.PointID, &p.UnitID, &p.Content); err != nil {
			return nil, fmt.Errorf("trace store: scan ambiguous unit point: %w", err)
		}
		byUnit[p.UnitID] = append(byUnit[p.UnitID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trace store: ambiguous unit points rows: %w", err)
	}

	for unitID, points := range byUnit {
		if len(points) > 1 {
			result[unitID] = points
		}
	}
	return result, nil
}

// PointContents returns each pointID's own knowledge_points.content — the
// evidence side of the constraint-consistency gate for unambiguous points
// (constraint.go / resolveDirectEvidence), replacing the old unit-level
// semantic summary. Points with no matching row are simply absent from the
// result (gate treats them as no-evidence-to-conflict-with, same as before).
func (s *Store) PointContents(pointIDs []string) (map[string]string, error) {
	result := make(map[string]string)
	if len(pointIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(pointIDs))
	args := make([]interface{}, len(pointIDs))
	for i, pid := range pointIDs {
		placeholders[i] = "?"
		args[i] = pid
	}

	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT point_id, content FROM knowledge_points WHERE point_id IN (%s)`,
		strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("trace store: point contents: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pointID, content string
		if err := rows.Scan(&pointID, &content); err != nil {
			return nil, fmt.Errorf("trace store: scan point content: %w", err)
		}
		result[pointID] = content
	}
	return result, rows.Err()
}

func (s *Store) ListCooccurrence(pointID string, minConfidentCount, limit int) ([]Cooccurrence, error) {
	query := `SELECT question_terms, point_id, hit_count, confident_count, last_seen_at
		FROM question_kp_cooccurrence WHERE 1=1`
	var args []interface{}

	if pointID != "" {
		query += ` AND point_id = ?`
		args = append(args, pointID)
	}
	if minConfidentCount > 0 {
		query += ` AND confident_count >= ?`
		args = append(args, minConfidentCount)
	}
	query += ` ORDER BY last_seen_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("trace store: list cooccurrence: %w", err)
	}
	defer rows.Close()

	var results []Cooccurrence
	for rows.Next() {
		var c Cooccurrence
		if err := rows.Scan(&c.QuestionTerms, &c.PointID, &c.HitCount, &c.ConfidentCount, &c.LastSeenAt); err != nil {
			return nil, fmt.Errorf("trace store: scan cooccurrence: %w", err)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

func (s *Store) ListLearningEvents(eventType string, processed, limit int) ([]LearningEvent, error) {
	query := `SELECT event_id, trace_id, event_type, payload, processed, created_at FROM learning_events WHERE 1=1`
	var args []interface{}

	if eventType != "" {
		query += ` AND event_type = ?`
		args = append(args, eventType)
	}
	if processed >= 0 {
		query += ` AND processed = ?`
		args = append(args, processed)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("trace store: list learning_events: %w", err)
	}
	defer rows.Close()

	var events []LearningEvent
	for rows.Next() {
		var e LearningEvent
		if err := rows.Scan(&e.EventID, &e.TraceID, &e.EventType, &e.Payload, &e.Processed, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("trace store: scan learning_event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
