package answer

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Save(r *AnswerResult, promptVersion, modelName string) error {
	citationsJSON, err := json.Marshal(r.Citations)
	if err != nil {
		return fmt.Errorf("answer store: marshal citations: %w", err)
	}
	snapshotJSON, err := json.Marshal(r.EvidenceSet)
	if err != nil {
		return fmt.Errorf("answer store: marshal evidence_snapshot: %w", err)
	}

	hasAnswer := 1
	if !r.HasAnswer {
		hasAnswer = 0
	}

	_, err = s.db.Exec(`INSERT INTO answers (answer_id, question, content, citations, evidence_snapshot, has_answer, path, prompt_version, model_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.AnswerID, r.Question, r.Content, string(citationsJSON), string(snapshotJSON),
		hasAnswer, r.Path, promptVersion, modelName,
	)
	if err != nil {
		return fmt.Errorf("answer store: insert: %w", err)
	}
	return nil
}

func (s *Store) Get(answerID string) (*AnswerResult, error) {
	var (
		citationsStr string
		snapshotStr  string
		hasAnswerInt int
		r            AnswerResult
	)
	err := s.db.QueryRow(`SELECT answer_id, question, content, citations, evidence_snapshot, has_answer, path, created_at
		FROM answers WHERE answer_id = ?`, answerID).
		Scan(&r.AnswerID, &r.Question, &r.Content, &citationsStr, &snapshotStr, &hasAnswerInt, &r.Path, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("answer store: get: %w", err)
	}

	if err := json.Unmarshal([]byte(citationsStr), &r.Citations); err != nil {
		return nil, fmt.Errorf("answer store: unmarshal citations: %w", err)
	}
	if err := json.Unmarshal([]byte(snapshotStr), &r.EvidenceSet); err != nil {
		return nil, fmt.Errorf("answer store: unmarshal evidence_snapshot: %w", err)
	}
	r.HasAnswer = hasAnswerInt == 1
	return &r, nil
}
