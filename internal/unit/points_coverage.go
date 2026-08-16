package unit

import (
	"regexp"
	"strings"
)

// isMarkdownTableRow matches a GFM table row (header, separator, or data row
// alike — syntactically identical: a line bounded by "|" on both ends).
// Duplicated in miniature from internal/evidence/service.go rather than
// shared cross-package — the same small duplication already exists between
// internal/source and internal/evidence for table/code-fence detection.
func isMarkdownTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return len(t) > 1 && strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|")
}

// isTableSeparatorRow matches a GFM header/body separator row, e.g.
// "| --- | --- |" or "|---|:---:|". These carry no content and must not be
// treated as a parallel item requiring coverage.
var tableSeparatorCellRE = regexp.MustCompile(`^:?-+:?$`)

func isTableSeparatorRow(line string) bool {
	t := strings.TrimSpace(line)
	if !isMarkdownTableRow(t) {
		return false
	}
	cells := strings.Split(strings.Trim(t, "|"), "|")
	for _, c := range cells {
		if !tableSeparatorCellRE.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

var listItemRE = regexp.MustCompile(`^\s*([-*]|\d+[.、)）])\s+\S`)
var listMarkerStripRE = regexp.MustCompile(`^\s*([-*]|\d+[.、)）])\s+`)

func isListItemLine(line string) bool {
	return listItemRE.MatchString(line)
}

var numberTokenRE = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// rowSignature is one parallel sub-item detected in a KU's raw content
// (a markdown table data row or a list item) that carries numeric values —
// the class of fact observed to get compressed away during point extraction
// when a category has many sibling sub-items.
type rowSignature struct {
	text    string // original line, for the verbatim-fallback path
	numbers []string
}

// detectNumericRowSignatures scans a KU's raw content lines for table data
// rows and list items, keeping only the ones that contain at least one
// number — non-numeric parallel prose is not the failure mode this guards
// against and checking it too would make coverage checks noisy.
func detectNumericRowSignatures(unitContent string) []rowSignature {
	var rows []rowSignature
	first := true
	for _, line := range strings.Split(unitContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isRow := false
		switch {
		case isMarkdownTableRow(trimmed):
			if isTableSeparatorRow(trimmed) {
				continue
			}
			// The first table row encountered is conventionally the header
			// row (column labels, not a data fact) — skip it.
			if first {
				first = false
				continue
			}
			isRow = true
		case isListItemLine(line):
			isRow = true
		}
		if !isRow {
			continue
		}
		nums := numberTokenRE.FindAllString(trimmed, -1)
		if len(nums) == 0 {
			continue
		}
		rows = append(rows, rowSignature{text: trimmed, numbers: dedupStrings(nums)})
	}
	return rows
}

func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// uncoveredRows returns the rowSignatures whose numeric tokens are not all
// present somewhere across the combined point content — i.e. rows whose
// specific numbers extraction dropped. All of a row's numbers must appear
// (not just one) so a row isn't marked covered by a partial, coincidental
// match.
func uncoveredRows(rows []rowSignature, points []llmPoint) []rowSignature {
	if len(rows) == 0 {
		return nil
	}
	var combined strings.Builder
	for _, p := range points {
		combined.WriteString(p.Content)
		combined.WriteByte('\n')
	}
	joined := combined.String()

	var missing []rowSignature
	for _, r := range rows {
		covered := true
		for _, n := range r.numbers {
			if !strings.Contains(joined, n) {
				covered = false
				break
			}
		}
		if !covered {
			missing = append(missing, r)
		}
	}
	return missing
}

// cleanRowText strips markdown table pipes/list markers from a row so it
// reads as plain content when used verbatim as a fallback point.
func cleanRowText(text string) string {
	t := strings.TrimSpace(text)
	if isMarkdownTableRow(t) {
		t = strings.Trim(t, "|")
		cells := strings.Split(t, "|")
		for i, c := range cells {
			cells[i] = strings.TrimSpace(c)
		}
		return strings.Join(cells, "，")
	}
	return listMarkerStripRE.ReplaceAllString(t, "")
}
