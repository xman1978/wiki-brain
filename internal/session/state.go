package session

import "encoding/json"

type ClarificationRecord struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Response string   `json:"response"`
	Turn     int      `json:"turn"`
}

type DialogueState struct {
	Topic             string                `json:"topic,omitempty"`
	Intent            string                `json:"intent"`
	Subject           string                `json:"subject"`
	RecentSubjects    []string              `json:"recent_subjects"`
	ClarificationLog  []ClarificationRecord `json:"clarification_log"`
}

type WorkingState struct {
	CurrentTask       string `json:"current_task,omitempty"`
	CurrentSubject    string `json:"current_subject"`
	StepSummary       string `json:"step_summary"`
	ContinuableAction string `json:"continuable_action"`
}

type SessionState struct {
	Dialogue DialogueState `json:"dialogue"`
	Working  WorkingState  `json:"working"`
}

type stateSnapshot struct {
	Intent            string                `json:"intent"`
	Subject           string                `json:"subject"`
	RecentSubjects    []string              `json:"recent_subjects"`
	ClarificationLog  []ClarificationRecord `json:"clarification_log"`
	CurrentSubject    string                `json:"current_subject"`
	StepSummary       string                `json:"step_summary"`
	ContinuableAction string                `json:"continuable_action"`
}

func (s *SessionState) MarshalSnapshot() (string, error) {
	snap := stateSnapshot{
		Intent:            s.Dialogue.Intent,
		Subject:           s.Dialogue.Subject,
		RecentSubjects:    s.Dialogue.RecentSubjects,
		ClarificationLog:  s.Dialogue.ClarificationLog,
		CurrentSubject:    s.Working.CurrentSubject,
		StepSummary:       s.Working.StepSummary,
		ContinuableAction: s.Working.ContinuableAction,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func (s *SessionState) UnmarshalSnapshot(data string) error {
	if data == "" || data == "{}" {
		return nil
	}
	var snap stateSnapshot
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		return err
	}
	s.Dialogue.Intent = snap.Intent
	s.Dialogue.Subject = snap.Subject
	s.Dialogue.RecentSubjects = snap.RecentSubjects
	s.Dialogue.ClarificationLog = snap.ClarificationLog
	s.Working.CurrentSubject = snap.CurrentSubject
	s.Working.StepSummary = snap.StepSummary
	s.Working.ContinuableAction = snap.ContinuableAction
	return nil
}

// API types

type TurnInput struct {
	SessionID string `json:"session_id"`
	UserInput string `json:"user_input"`
}

type TurnResult struct {
	SessionID     string               `json:"session_id"`
	Action        string               `json:"action"`
	ExpandedQuery *ExpandedQuery       `json:"expanded_query,omitempty"`
	Clarification *ClarificationPrompt `json:"clarification,omitempty"`
	StateSnapshot *SessionState        `json:"state_snapshot,omitempty"`
}

type ClarifyInput struct {
	SessionID   string `json:"session_id"`
	SelectedRef string `json:"selected_ref"`
}

type ClarifyResult struct {
	Action        string         `json:"action"`
	ExpandedQuery *ExpandedQuery `json:"expanded_query,omitempty"`
}

type ExpandedQuery struct {
	OriginalInput      string   `json:"original_input"`
	ExpandedQuestion   string   `json:"expanded_question"`
	Subject            string   `json:"subject,omitempty"`
	Intent             string   `json:"intent"`
	DefaultAssumptions []string `json:"default_assumptions"`
	AllowRetrieval     bool     `json:"allow_retrieval"`
}

type ClarificationPrompt struct {
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

type Option struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type WorkingInput struct {
	SessionID         string `json:"session_id"`
	StepSummary       string `json:"step_summary"`
	ContinuableAction string `json:"continuable_action"`
}

type SessionInfo struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

type TurnInfo struct {
	TurnID     string `json:"turn_id"`
	TurnIndex  int    `json:"turn_index"`
	UserInput  string `json:"user_input"`
	Action     string `json:"action"`
	AnswerID   string `json:"answer_id,omitempty"`
	ClarifyMsg string `json:"clarify_msg,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type ParseResult struct {
	Intent  string
	Subject string
}
