package session

import "strings"

func Expand(state *SessionState, plan PlanResult, input string) ExpandedQuery {
	eq := ExpandedQuery{
		OriginalInput:  input,
		Subject:        plan.Subject,
		Intent:         state.Dialogue.Intent,
		Constraint:     state.Dialogue.Constraint,
		AllowRetrieval: true,
	}

	if state.Working.ContinuableAction != "" && plan.Action == PlanRetrieve && plan.Subject == "" {
		eq.ExpandedQuestion = state.Working.ContinuableAction
		eq.DefaultAssumptions = []string{"综合角度"}
		return eq
	}

	inputRunes := []rune(input)
	built := buildIntentSubjectQuery(state.Dialogue.Intent, plan.Subject)

	if built != "" && len(inputRunes) < len([]rune(built)) {
		eq.ExpandedQuestion = built
	} else {
		eq.ExpandedQuestion = input
	}

	if eq.ExpandedQuestion == "" {
		eq.ExpandedQuestion = input
	}

	eq.DefaultAssumptions = []string{"综合角度"}
	return eq
}

func buildIntentSubjectQuery(intent, subject string) string {
	if intent != "" && subject != "" {
		if strings.Contains(intent, subject) {
			return intent
		}
		return subject + " " + intent
	}
	if intent != "" {
		return intent
	}
	if subject != "" {
		return subject
	}
	return ""
}

func UpdateTopic(state *SessionState) {
	if state.Dialogue.Subject != "" {
		state.Dialogue.Topic = state.Dialogue.Intent + " - " + state.Dialogue.Subject
	} else if state.Working.CurrentSubject != "" {
		state.Dialogue.Topic = state.Dialogue.Intent + " - " + state.Working.CurrentSubject
	} else {
		state.Dialogue.Topic = state.Dialogue.Intent
	}
}
