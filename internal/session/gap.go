package session

type GapKind string

const (
	GapVague GapKind = "vague"
)

func DetectGaps(parsed ParseResult, state *SessionState, userInput string) []GapKind {
	if parsed.Subject != "" || state.Working.CurrentSubject != "" {
		return nil
	}
	return []GapKind{GapVague}
}
