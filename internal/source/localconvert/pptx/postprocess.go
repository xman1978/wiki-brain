package pptx

import (
	"regexp"
	"strings"
)

var (
	blankLineRe     = regexp.MustCompile(`^\s*$`)
	trailingSpaceRe = regexp.MustCompile(`[ \t]+$`)
)

// PostprocessText normalises line endings, trims trailing whitespace, and
// collapses runs of 2+ blank lines to one.
func PostprocessText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = trailingSpaceRe.ReplaceAllString(line, "")
		if blankLineRe.MatchString(line) {
			blank++
			if blank == 1 {
				out = append(out, "")
			}
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
