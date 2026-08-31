package localconvert

// Ports docs/impl/v1/docx-port/01-word-to-markdown.md §8 (table conversion):
// §8.1 convertSingleCellTableToText, §8.2 convertTableToMarkdown (with
// horizontal/vertical merge expansion), §8.3 mergeDuplicateHeaderRows.

import (
	"strings"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

func cellPlainText(paragraphs []docParagraph) string {
	var parts []string
	for _, p := range paragraphs {
		t := cleanCellParagraphText(p.Text)
		t = strings.TrimSpace(t)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func cleanCellParagraphText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\f", "")
	return s
}

// convertSingleCellTableToText ports docx-port/01 §8.1.
func convertSingleCellTableToText(tbl *docTable) string {
	if len(tbl.Rows) != 1 || len(tbl.Rows[0]) != 1 {
		return ""
	}
	cell := tbl.Rows[0][0]
	var paragraphLines []string
	for _, p := range cell.Paragraphs {
		t := strings.TrimSpace(cleanCellParagraphText(p.Text))
		if t != "" {
			paragraphLines = append(paragraphLines, t)
		}
	}
	if len(paragraphLines) == 0 {
		return ""
	}
	if pdfconv.LooksLikePreformattedBlock(paragraphLines) {
		return pdfconv.WrapCodeFence(paragraphLines)
	}
	return strings.Join(paragraphLines, " ") + "\n"
}

// expandHorizontalMerges mirrors table.convertToHorizontallyMergedCells():
// a cell with gridSpan=N is expanded into N logical cells, all carrying the
// same text (docx-port/01 §8.2 — "合并单元格拆分为独立单元格填充内容，而
// 不是留空").
func expandHorizontalMerges(row []docCell) []docCell {
	var out []docCell
	for _, c := range row {
		n := c.GridSpan
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, c)
		}
	}
	return out
}

// renderTable ports the top of docx-port/01 §8.2 ("先排除 8.1 的单格特例")
// — the dispatcher docx_blocks.go should call.
func renderTable(tbl *docTable) string {
	if len(tbl.Rows) == 1 && len(tbl.Rows[0]) == 1 {
		return convertSingleCellTableToText(tbl)
	}
	return convertTableToMarkdown(tbl)
}

// convertTableToMarkdown ports docx-port/01 §8.2 (multi-cell case only —
// callers should route single-cell tables through
// convertSingleCellTableToText / renderTable first).
func convertTableToMarkdown(tbl *docTable) string {
	var rows [][]string
	var prevRow []string
	for _, rawRow := range tbl.Rows {
		expanded := expandHorizontalMerges(rawRow)
		var cols []string
		for colIdx, cell := range expanded {
			text := cellPlainText(cell.Paragraphs)
			if text == "" && cell.VMergeCont {
				if prevRow != nil && colIdx < len(prevRow) {
					text = prevRow[colIdx]
				}
			} else if text == "" && colIdx > 0 && isHorizontalContinuation(expanded, colIdx) {
				text = cols[len(cols)-1]
			}
			cols = append(cols, text)
		}
		rows = append(rows, cols)
		prevRow = cols
	}
	if len(rows) == 0 {
		return ""
	}
	rows = mergeDuplicateHeaderRows(rows)

	colCount := 0
	for _, r := range rows {
		if len(r) > colCount {
			colCount = len(r)
		}
	}
	for i := range rows {
		for len(rows[i]) < colCount {
			rows[i] = append(rows[i], "")
		}
	}

	var sb strings.Builder
	sb.WriteString("\n")
	writeRow := func(r []string) {
		sb.WriteString("|")
		for _, c := range r {
			sb.WriteString(" ")
			sb.WriteString(c)
			sb.WriteString(" |")
		}
		sb.WriteString("\n")
	}
	writeRow(rows[0])
	sb.WriteString("|")
	for i := 0; i < colCount; i++ {
		sb.WriteString(" --- |")
	}
	sb.WriteString("\n")
	for _, r := range rows[1:] {
		writeRow(r)
	}
	return sb.String()
}

// isHorizontalContinuation reports whether the cell at colIdx in the
// already-expanded row is a gridSpan continuation slot (i.e. this column
// index is not the first occurrence of that same *docCell within the
// expanded row). Used as a fallback for a same-row horizontal-merge
// continuation whose own text also happens to be empty.
func isHorizontalContinuation(expanded []docCell, colIdx int) bool {
	if colIdx == 0 {
		return false
	}
	// Since expandHorizontalMerges duplicates the identical docCell value
	// for every spanned column, compare by paragraph text identity: if the
	// previous column's source cell has the same (already-empty) text, this
	// is a continuation.
	return sameCellSource(expanded[colIdx], expanded[colIdx-1])
}

func sameCellSource(a, b docCell) bool {
	return a.GridSpan == b.GridSpan && cellPlainText(a.Paragraphs) == cellPlainText(b.Paragraphs)
}

// normalizeRow ports docx-port/01 §8.3 normalizeRow.
func normalizeRow(row []string) string {
	var sb strings.Builder
	for i, c := range row {
		if i > 0 {
			sb.WriteString("\x1f")
		}
		t := strings.TrimSpace(c)
		t = strings.ReplaceAll(t, "　", " ")
		t = collapseSpaceRuns(t)
		sb.WriteString(t)
	}
	return sb.String()
}

func collapseSpaceRuns(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// mergeDuplicateHeaderRows ports docx-port/01 §8.3.
func mergeDuplicateHeaderRows(rows [][]string) [][]string {
	if len(rows) < 2 {
		return rows
	}
	first := normalizeRow(rows[0])
	removeCount := 0
	for i := 1; i < len(rows); i++ {
		if normalizeRow(rows[i]) == first {
			removeCount++
			continue
		}
		break
	}
	if removeCount == 0 {
		return rows
	}
	out := append([][]string{rows[0]}, rows[1+removeCount:]...)
	return out
}
