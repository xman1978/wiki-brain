package session

type PlanAction string

const (
	PlanRetrieve PlanAction = "retrieve"
	PlanClarify  PlanAction = "clarify"
	PlanSkip     PlanAction = "skip"
)

type PlanResult struct {
	Action        PlanAction
	Clarification *ClarificationPrompt
	Subject       string
}

func Plan(gaps []GapKind, parsed ParseResult, state *SessionState) PlanResult {
	if len(gaps) == 0 {
		subject := parsed.Subject
		if subject == "" {
			subject = state.Working.CurrentSubject
		}
		return PlanResult{Action: PlanRetrieve, Subject: subject}
	}

	if shouldSkipClarification(gaps, state) {
		return PlanResult{Action: PlanRetrieve}
	}

	// GapVague: intent is vague and no subject available
	if len(state.Dialogue.RecentSubjects) > 0 {
		recent := state.Dialogue.RecentSubjects
		if len(recent) > 3 {
			recent = recent[:3]
		}
		var options []Option
		for _, s := range recent {
			options = append(options, Option{Ref: s, Label: s})
		}
		return PlanResult{
			Action: PlanClarify,
			Clarification: &ClarificationPrompt{
				Question: "您想了解哪个主题？",
				Options:  options,
			},
		}
	}

	return PlanResult{
		Action: PlanClarify,
		Clarification: &ClarificationPrompt{
			Question: "请描述您想了解的内容",
			Options:  []Option{},
		},
	}
}

func shouldSkipClarification(gaps []GapKind, state *SessionState) bool {
	if len(gaps) == 0 || len(state.Dialogue.ClarificationLog) == 0 {
		return false
	}
	last := state.Dialogue.ClarificationLog[len(state.Dialogue.ClarificationLog)-1]
	return last.Response == "refused"
}
