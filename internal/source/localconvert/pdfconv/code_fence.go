package pdfconv

import (
	"regexp"
	"strings"
)

// CodeFenceWriter port (pdf-port/04-toplevel-heuristics.md "CodeFenceWriter"
// section).

var backtickRunRe = regexp.MustCompile("`+")

func longestBacktickRun(text string) int {
	longest := 0
	for _, m := range backtickRunRe.FindAllString(text, -1) {
		if len(m) > longest {
			longest = len(m)
		}
	}
	return longest
}

// WrapCodeFence ports CodeFenceWriter.wrap.
func WrapCodeFence(lines []string) string {
	body := strings.Join(lines, "\n")
	fenceLen := longestBacktickRun(body) + 1
	if fenceLen < 3 {
		fenceLen = 3
	}
	fence := strings.Repeat("`", fenceLen)
	return fence + "\n" + body + "\n" + fence + "\n"
}
