package source

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reHeading       = regexp.MustCompile(`^(#{1,6})\s`)
	reMultiBlank    = regexp.MustCompile(`\n{3,}`)
	reHTMLComment   = regexp.MustCompile(`<!--[\s\S]*?-->`)
	reZeroWidth     = regexp.MustCompile(`[\x{200B}\x{200C}\x{200D}\x{FEFF}]`)
)

func NormalizeMarkdown(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	content = reZeroWidth.ReplaceAllString(content, "")
	content = reHTMLComment.ReplaceAllString(content, "")

	lines := strings.Split(content, "\n")
	lines = normalizeHeadingLevels(lines)

	content = strings.Join(lines, "\n")
	content = reMultiBlank.ReplaceAllString(content, "\n\n")
	content = strings.TrimSpace(content)

	return content
}

func normalizeHeadingLevels(lines []string) []string {
	inCodeBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		if m := reHeading.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if level > 4 {
				// Demote to H4
				rest := strings.TrimPrefix(line, m[1])
				lines[i] = "####" + rest
			}
		}
	}

	lines = insertVirtualHeadings(lines)
	return lines
}

func insertVirtualHeadings(lines []string) []string {
	type headingInfo struct {
		lineIdx int
		level   int
	}

	var headings []headingInfo
	inCodeBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		if m := reHeading.FindStringSubmatch(line); m != nil {
			headings = append(headings, headingInfo{lineIdx: i, level: len(m[1])})
		}
	}

	if len(headings) < 2 {
		return lines
	}

	// Find gaps: e.g., H1 followed by H3 means we need to insert H2
	var inserts []struct {
		afterIdx int
		level    int
	}

	prevLevel := 0
	for _, h := range headings {
		if prevLevel > 0 && h.level > prevLevel+1 {
			for gap := prevLevel + 1; gap < h.level; gap++ {
				inserts = append(inserts, struct {
					afterIdx int
					level    int
				}{h.lineIdx, gap})
			}
		}
		prevLevel = h.level
	}

	if len(inserts) == 0 {
		return lines
	}

	// Insert virtual headings before the lines that have gaps
	// Process in reverse order to maintain indices
	for i := len(inserts) - 1; i >= 0; i-- {
		ins := inserts[i]
		virtualLine := strings.Repeat("#", ins.level) + " "
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:ins.afterIdx]...)
		newLines = append(newLines, virtualLine)
		newLines = append(newLines, lines[ins.afterIdx:]...)
		lines = newLines
	}

	return lines
}

func RuneCount(s string) int {
	return utf8.RuneCountInString(s)
}
