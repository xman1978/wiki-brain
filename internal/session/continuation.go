package session

import "strings"

var continuationKeywords = []string{
	"继续", "下一个", "再看一个", "按刚才", "基于上面",
}

func DetectContinuation(input string, state *SessionState) bool {
	if state.Working.ContinuableAction == "" {
		return false
	}
	trimmed := strings.TrimSpace(input)
	for _, kw := range continuationKeywords {
		if trimmed == kw || strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}
