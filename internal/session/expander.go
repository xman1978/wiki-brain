package session

func Expand(state *SessionState, plan PlanResult, input string) ExpandedQuery {
	eq := ExpandedQuery{
		OriginalInput:  input,
		Subject:        plan.Subject,
		Intent:         state.Dialogue.Intent,
		AllowRetrieval: true,
	}

	if state.Working.ContinuableAction != "" && plan.Action == PlanRetrieve && plan.Subject == "" {
		eq.ExpandedQuestion = state.Working.ContinuableAction
		return eq
	}

	// Use original input as the primary query — it contains the full natural language
	// with all keywords the retrieval system needs
	eq.ExpandedQuestion = input

	if eq.ExpandedQuestion == "" {
		if plan.Subject != "" && state.Dialogue.Intent != "" {
			eq.ExpandedQuestion = state.Dialogue.Intent + " " + plan.Subject
		} else if state.Dialogue.Intent != "" {
			eq.ExpandedQuestion = state.Dialogue.Intent
		}
	}

	eq.DefaultAssumptions = []string{"综合角度"}
	return eq
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
