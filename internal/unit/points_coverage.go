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
//
// label is set only for column signatures (see detectColumnSignatures): it
// names the category the numbers belong to (e.g. a table column header like
// "D 类城市") when that category's own definition lives elsewhere in the
// unit (e.g. a numbered list below the table) rather than on the same line
// as the numbers. A row with a label is only "covered" by a point whose
// content mentions the label *and* the number together — otherwise a point
// could satisfy plain numeric coverage by stating the number in an unrelated
// sentence while the category's definition-only point stays disconnected
// from it (the "D 类城市: 除...其他所有城市" without "200元" failure mode).
type rowSignature struct {
	text    string // original line, for the verbatim-fallback path
	numbers []string
	label   string
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

func splitTableCells(row string) []string {
	t := strings.Trim(strings.TrimSpace(row), "|")
	cells := strings.Split(t, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

// detectColumnSignatures scans a KU's raw content for a markdown table and
// pairs each non-first header cell (a category label, e.g. "D 类城市") with
// the corresponding cell in each data row (the category's value, e.g.
// "200元") by column position. Unlike detectNumericRowSignatures, which
// treats a whole row/list-item as one unit, this catches the case where a
// category's numeric value sits in a table cell while the category's own
// definition is written out separately elsewhere in the unit (commonly a
// numbered list below the table) — the two never share a line, so neither
// detectNumericRowSignatures nor a per-line number check would ever notice
// the definition-only point is missing its number.
func detectColumnSignatures(unitContent string) []rowSignature {
	var header []string
	var signatures []rowSignature
	for _, line := range strings.Split(unitContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if !isMarkdownTableRow(trimmed) || isTableSeparatorRow(trimmed) {
			continue
		}
		cells := splitTableCells(trimmed)
		if header == nil {
			header = cells
			continue
		}
		for i := 1; i < len(cells) && i < len(header); i++ {
			label := header[i]
			value := cells[i]
			if label == "" || value == "" {
				continue
			}
			nums := numberTokenRE.FindAllString(value, -1)
			if len(nums) == 0 {
				continue
			}
			signatures = append(signatures, rowSignature{
				text:    label + "：" + value,
				numbers: dedupStrings(nums),
				label:   label,
			})
		}
	}
	return signatures
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

// uncoveredRows returns the rowSignatures that extraction dropped. For a
// plain row (no label), all of its numeric tokens must appear somewhere
// across the combined point content — not just one — so a row isn't marked
// covered by a partial, coincidental match. For a column signature (label
// set), the label and every number must appear together in the *same*
// point's content, since the point that states the number but not the label
// (or vice versa) doesn't actually let a reader answer "what's the value for
// this category" — the fact the number exists somewhere in the unit is not
// the same as the category being self-contained.
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
		var covered bool
		if r.label != "" {
			covered = coveredTogether(points, r.label, r.numbers)
		} else {
			covered = true
			for _, n := range r.numbers {
				if !strings.Contains(joined, n) {
					covered = false
					break
				}
			}
		}
		if !covered {
			missing = append(missing, r)
		}
	}
	return missing
}

// coveredTogether reports whether some single point's content mentions both
// label and every number in nums.
func coveredTogether(points []llmPoint, label string, nums []string) bool {
	for _, p := range points {
		if !strings.Contains(p.Content, label) {
			continue
		}
		allPresent := true
		for _, n := range nums {
			if !strings.Contains(p.Content, n) {
				allPresent = false
				break
			}
		}
		if allPresent {
			return true
		}
	}
	return false
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
