package session

import "strings"

var interruptKeywords = []string{
	"停", "停下", "别说了", "不用了", "清空", "重来", "重置", "从头", "算了重来",
}

// maxInterruptInputLen 限制打断词匹配只在极短的独立发话中生效。真正的打断都是这种
// 简短表达；超过该长度的输入视为正常问题（即使碰巧包含"停"等单字，也不应被当作打断）。
const maxInterruptInputLen = 6

func DetectInterrupt(input string) bool {
	trimmed := strings.TrimSpace(input)
	if len([]rune(trimmed)) > maxInterruptInputLen {
		return false
	}
	for _, kw := range interruptKeywords {
		if trimmed == kw || strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}
