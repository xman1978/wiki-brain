package trace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

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

	hasFeedback := 0
	if t.HasFeedback {
		hasFeedback = 1
	}

	_, err = s.db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms,
		retrieval_quality, path, direct_point_ids, has_feedback, feedback_type, feedback_content)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TraceID, t.AnswerID, t.Question, t.QuestionHash, t.QuestionTerms,
		t.RetrievalQuality, t.Path, string(pointIDsJSON), hasFeedback,
		nullString(t.FeedbackType), nullString(t.FeedbackContent),
	)
	if err != nil {
		return fmt.Errorf("trace store: insert: %w", err)
	}
	return nil
}

func (s *Store) GetTrace(traceID string) (*Trace, error) {
	var (
		t             Trace
		pointIDsStr   string
		hasFeedbackInt int
		feedbackType  sql.NullString
		feedbackContent sql.NullString
	)
	err := s.db.QueryRow(`SELECT trace_id, answer_id, question, question_hash, question_terms,
		retrieval_quality, path, direct_point_ids, has_feedback, feedback_type, feedback_content,
		created_at, updated_at
		FROM traces WHERE trace_id = ?`, traceID).
		Scan(&t.TraceID, &t.AnswerID, &t.Question, &t.QuestionHash, &t.QuestionTerms,
			&t.RetrievalQuality, &t.Path, &pointIDsStr, &hasFeedbackInt,
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
	t.HasFeedback = hasFeedbackInt == 1
	t.FeedbackType = feedbackType.String
	t.FeedbackContent = feedbackContent.String
	return &t, nil
}

func (s *Store) ListTraces(quality, answerID string, limit, offset int) ([]Trace, error) {
	query := `SELECT trace_id, answer_id, question, retrieval_quality, has_feedback, created_at FROM traces WHERE 1=1`
	var args []interface{}

	if quality != "" {
		query += ` AND retrieval_quality = ?`
		args = append(args, quality)
	}
	if answerID != "" {
		query += ` AND answer_id = ?`
		args = append(args, answerID)
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
		var hasFeedbackInt int
		if err := rows.Scan(&t.TraceID, &t.AnswerID, &t.Question, &t.RetrievalQuality, &hasFeedbackInt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("trace store: scan: %w", err)
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

func (s *Store) SaveLearningEvent(traceID, eventType, payload string) error {
	eventID := uuid.New().String()
	_, err := s.db.Exec(`INSERT INTO learning_events (event_id, trace_id, event_type, payload) VALUES (?, ?, ?, ?)`,
		eventID, traceID, eventType, payload)
	if err != nil {
		return fmt.Errorf("trace store: insert learning_event: %w", err)
	}
	return nil
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

