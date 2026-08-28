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
	rePivotTextRow  = regexp.MustCompile(`^id=\S+\s*\|\s*表=.*\|\s*data=\{.*\}$`)
)

func NormalizeMarkdown(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	content = reZeroWidth.ReplaceAllString(content, "")
	content = reHTMLComment.ReplaceAllString(content, "")

	lines := strings.Split(content, "\n")
	lines = stripPivotTextDuplicate(lines)
	lines = normalizeHeadingLevels(lines)

	content = strings.Join(lines, "\n")
	content = reMultiBlank.ReplaceAllString(content, "\n\n")
	content = strings.TrimSpace(content)

	return content
}

// stripPivotTextDuplicate 去除 FileView 对 Excel 表格做 pivot 转换时输出的重复内容：
// 一个 ```json 围栏块（结构化 schema+data）后紧跟一个 ```text 围栏块，后者是同一批
// 数据按 `id=... | 表=... | data={...}` 逐行展开的冗余文本表示。只保留 json 块，避免
// 同一条数据被 Unit 抽取两遍产生重复 KP。
func stripPivotTextDuplicate(lines []string) []string {
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "```json" {
			jsonEnd := findFenceEnd(lines, i+1)
			if jsonEnd != -1 {
				j := jsonEnd + 1
				for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
					j++
				}
				if j < len(lines) && strings.TrimSpace(lines[j]) == "```text" {
					textEnd := findFenceEnd(lines, j+1)
					if textEnd != -1 && isPivotTextBlock(lines[j+1:textEnd]) {
						out = append(out, lines[i:jsonEnd+1]...)
						i = textEnd + 1
						continue
					}
				}
			}
		}
		out = append(out, lines[i])
		i++
	}
	return out
}

func findFenceEnd(lines []string, start int) int {
	for k := start; k < len(lines); k++ {
		if strings.TrimSpace(lines[k]) == "```" {
			return k
		}
	}
	return -1
}

func isPivotTextBlock(lines []string) bool {
	found := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if !rePivotTextRow.MatchString(t) {
			return false
		}
		found = true
	}
	return found
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
