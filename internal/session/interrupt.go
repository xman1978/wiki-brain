package session

import "strings"

var interruptKeywords = []string{
	"停", "停下", "别说了", "不用了", "清空", "重来", "重置", "从头", "算了重来",
}

func DetectInterrupt(input string) bool {
	trimmed := strings.TrimSpace(input)
	for _, kw := range interruptKeywords {
		if trimmed == kw || strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}
