package unit

import (
	"strconv"
	"strings"
)

// gapRanges returns the contiguous line ranges within [segStart, segEnd] not
// covered by any of the given (lineStart, lineEnd) unit ranges — the same
// algorithm ComputeCoverage uses, but operating on in-memory ranges from the
// extraction pass just run instead of re-reading units back from SQLite.
func gapRanges(segStart, segEnd int, covered [][2]int) [][2]int {
	total := segEnd - segStart + 1
	if total <= 0 {
		return nil
	}
	isCovered := make([]bool, total)
	for _, r := range covered {
		from, to := r[0], r[1]
		if from < segStart {
			from = segStart
		}
		if to > segEnd {
			to = segEnd
		}
		for line := from; line <= to; line++ {
			isCovered[line-segStart] = true
		}
	}

	var gaps [][2]int
	start := -1
	for i := 0; i <= total; i++ {
		ok := i < total && isCovered[i]
		if i < total && !ok {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			gaps = append(gaps, [2]int{segStart + start, segStart + i - 1})
			start = -1
		}
	}
	return gaps
}

// isTrivialGap reports whether a gap's raw text is pure markdown scaffolding
// (headings, blank lines, horizontal rules, table separator rows) with no
// content of its own — safe to merge into a neighboring unit without
// spending an LLM call to find out. Anything else is sent through
// extractGap so the model itself judges whether it deserves its own unit.
func isTrivialGap(mdLines []string, lineStart, lineEnd int) bool {
	for i := lineStart; i <= lineEnd; i++ {
		if i < 1 || i > len(mdLines) {
			continue
		}
		line := strings.TrimSpace(mdLines[i-1])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if isRuleOrSeparatorRow(line) {
			continue
		}
		return false
	}
	return true
}

// isRuleOrSeparatorRow matches markdown table separator rows ("| --- | --- |")
// and horizontal rules ("---", "==="): punctuation-only lines with nothing
// extractable in them.
func isRuleOrSeparatorRow(line string) bool {
	stripped := strings.Map(func(r rune) rune {
		switch r {
		case '|', '-', ':', ' ', '=':
			return -1
		}
		return r
	}, line)
	return stripped == ""
}

// gapContextText renders the gap's own lines inside the rest of the parent
// segment, clearly marked off from the surrounding context on both sides so
// the model can use the full segment (an enclosing IF/loop header, a
// preceding definition, etc.) to understand what the gap's lines mean
// without mistaking the context for something it should also extract units
// from. When the gap is the entire segment (no context on either side), this
// degrades to plain numbered lines, same as sliceLinesWithLineNumbers.
func gapContextText(mdLines []string, seg Segment, gapStart, gapEnd int) string {
	var sb strings.Builder
	if gapStart > seg.LineStart {
		sb.WriteString("[以下第 " + strconv.Itoa(seg.LineStart) + "-" + strconv.Itoa(gapStart-1) + " 行是上下文，仅供理解，不要在这些行上生成 unit]\n")
		sb.WriteString(sliceLinesWithLineNumbers(mdLines, seg.LineStart, gapStart-1))
	}
	sb.WriteString("[以下第 " + strconv.Itoa(gapStart) + "-" + strconv.Itoa(gapEnd) + " 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n")
	sb.WriteString(sliceLinesWithLineNumbers(mdLines, gapStart, gapEnd))
	if gapEnd < seg.LineEnd {
		sb.WriteString("[以下第 " + strconv.Itoa(gapEnd+1) + "-" + strconv.Itoa(seg.LineEnd) + " 行是上下文，仅供理解，不要在这些行上生成 unit]\n")
		sb.WriteString(sliceLinesWithLineNumbers(mdLines, gapEnd+1, seg.LineEnd))
	}
	return sb.String()
}

// gapExtractOutput is unit_gap_extract.md's response: an explicit placement
// decision for the uncovered lines, plus units/points only when the decision
// is standalone.
type gapExtractOutput struct {
	Action string     `json:"action"`
	Units  []llmUnit  `json:"units"`
	Points []llmPoint `json:"points"`
}

// Actions unit_gap_extract.md can return. absorb_left/right direct the gap
// into a specific neighbor; standalone means the gap deserves its own
// unit(s); skip marks metadata/decoration (still absorbed into the nearest
// neighbor by the program, purely to keep line coverage tiled).
const (
	gapActionAbsorbLeft  = "absorb_left"
	gapActionAbsorbRight = "absorb_right"
	gapActionStandalone  = "standalone"
	gapActionSkip        = "skip"
)
