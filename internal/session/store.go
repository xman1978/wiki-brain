package session

import (
	"database/sql"
	"sync"

	"github.com/google/uuid"
)

type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]*SessionState
}

func NewStore(db *sql.DB) *Store {
	return &Store{
		db:    db,
		cache: make(map[string]*SessionState),
	}
}

func (s *Store) Get(sessionID string) (*SessionState, error) {
	s.mu.RLock()
	if st, ok := s.cache[sessionID]; ok {
		s.mu.RUnlock()
		return st, nil
	}
	s.mu.RUnlock()

	var snapshot string
	err := s.db.QueryRow(`SELECT state_snapshot FROM sessions WHERE session_id = ?`, sessionID).Scan(&snapshot)
	if err == sql.ErrNoRows {
		st := &SessionState{}
		s.mu.Lock()
		s.cache[sessionID] = st
		s.mu.Unlock()
		return st, nil
	}
	if err != nil {
		return nil, err
	}

	st := &SessionState{}
	if err := st.UnmarshalSnapshot(snapshot); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[sessionID] = st
	s.mu.Unlock()
	return st, nil
}

func (s *Store) Set(sessionID string, state *SessionState) error {
	s.mu.Lock()
	s.cache[sessionID] = state
	s.mu.Unlock()

	snap, err := state.MarshalSnapshot()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE sessions SET state_snapshot = ?, updated_at = CURRENT_TIMESTAMP WHERE session_id = ?`, snap, sessionID)
	return err
}

func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	delete(s.cache, sessionID)
	s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM sessions WHERE session_id = ?`, sessionID)
	return err
}

func (s *Store) CreateSession() (SessionInfo, error) {
	id := uuid.New().String()
	var info SessionInfo
	_, err := s.db.Exec(`INSERT INTO sessions (session_id) VALUES (?)`, id)
	if err != nil {
		return info, err
	}
	err = s.db.QueryRow(`SELECT session_id, title, created_at FROM sessions WHERE session_id = ?`, id).
		Scan(&info.SessionID, &info.Title, &info.CreatedAt)
	return info, err
}

func (s *Store) ListSessions() ([]SessionInfo, error) {
	rows, err := s.db.Query(`SELECT session_id, title, updated_at, created_at FROM sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []SessionInfo
	for rows.Next() {
		var si SessionInfo
		if err := rows.Scan(&si.SessionID, &si.Title, &si.UpdatedAt, &si.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, si)
	}
	return list, rows.Err()
}

func (s *Store) ListTurns(sessionID string) ([]TurnInfo, error) {
	rows, err := s.db.Query(`SELECT turn_id, turn_index, user_input, action, COALESCE(answer_id,''), COALESCE(clarify_msg,''), created_at
		FROM session_turns WHERE session_id = ? ORDER BY turn_index ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TurnInfo
	for rows.Next() {
		var t TurnInfo
		if err := rows.Scan(&t.TurnID, &t.TurnIndex, &t.UserInput, &t.Action, &t.AnswerID, &t.ClarifyMsg, &t.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

func (s *Store) InsertTurn(sessionID, userInput, action, clarifyMsg string) error {
	turnID := uuid.New().String()
	_, err := s.db.Exec(`INSERT INTO session_turns (turn_id, session_id, turn_index, user_input, action, clarify_msg)
		VALUES (?, ?, (SELECT COALESCE(MAX(turn_index),0)+1 FROM session_turns WHERE session_id=?), ?, ?, ?)`,
		turnID, sessionID, sessionID, userInput, action, clarifyMsg)
	return err
}

func (s *Store) UpdateTitle(sessionID, title string) error {
	_, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE session_id = ? AND title = ''`, title, sessionID)
	return err
}

func (s *Store) UpdateWorking(sessionID, stepSummary, continuableAction string) error {
	state, err := s.Get(sessionID)
	if err != nil {
		return err
	}
	state.Working.StepSummary = stepSummary
	state.Working.ContinuableAction = continuableAction
	return s.Set(sessionID, state)
}
