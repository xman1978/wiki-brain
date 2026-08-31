package localconvert

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// clamp mirrors Java's simple min/max clamp helper usage inline in
// detectHeaderRowsWithStyle.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// detectHeaderRowsWithStyle mirrors detectHeaderRowsWithStyle: take the max
// of the content-based and font-style-based guesses, clamped to [1,10].
func detectHeaderRowsWithStyle(f *excelize.File, sheet string, matrix [][]string, maxRow, maxCol int) int {
	contentGuess := detectHeaderRows(matrix)
	styleGuess := detectHeaderRowsByFontStyle(f, sheet, matrix, maxRow, maxCol)
	guess := maxInt(contentGuess, styleGuess)
	return clamp(guess, 1, 10)
}

// detectHeaderRows mirrors detectHeaderRows: pure content heuristic over the
// first 3 rows, no comma-stripping before the numeric check.
func detectHeaderRows(matrix [][]string) int {
	rows := minInt(3, len(matrix))
	for i := 0; i < rows; i++ {
		total := 0
		textCount := 0
		for _, v := range matrix[i] {
			if v == "" {
				continue
			}
			total++
			if !isNumeric(v) {
				textCount++
			}
		}
		if total > 0 && float64(textCount)/float64(total) > 0.6 {
			return i + 1
		}
	}
	return 1
}

type fontSig struct {
	name string
	size int
	bold bool
}

// getFontSig mirrors fontSig(cell): any lookup failure yields the zero
// FontSig, matching the Java try/catch-and-swallow behavior.
func getFontSig(f *excelize.File, sheet string, row, col int) fontSig {
	cellRef, err := excelize.CoordinatesToCellName(col+1, row+1)
	if err != nil {
		return fontSig{}
	}
	styleID, err := f.GetCellStyle(sheet, cellRef)
	if err != nil {
		return fontSig{}
	}
	style, err := f.GetStyle(styleID)
	if err != nil || style == nil || style.Font == nil {
		return fontSig{}
	}
	return fontSig{
		name: style.Font.Family,
		size: int(style.Font.Size + 0.5), // Math.round
		bold: style.Font.Bold,
	}
}

// detectHeaderRowsByFontStyle mirrors detectHeaderRowsByFontStyle.
func detectHeaderRowsByFontStyle(f *excelize.File, sheet string, matrix [][]string, maxRow, maxCol int) int {
	if maxRow < 0 || maxCol < 0 {
		return 1
	}
	scanTop := minInt(10, maxRow+1)
	bodyStart := minInt(maxInt(2, scanTop), maxInt(2, maxRow/10))
	bodyEnd := minInt(maxRow, bodyStart+20)

	var bodySigOrder []fontSig
	bodyFreq := map[fontSig]int{}
	bodyNonEmpty := 0
	for i := bodyStart; i <= bodyEnd && i <= maxRow; i++ {
		if i < 0 || i >= len(matrix) {
			continue
		}
		for j := 0; j <= maxCol && j < len(matrix[i]); j++ {
			if matrix[i][j] == "" {
				continue
			}
			bodyNonEmpty++
			sig := getFontSig(f, sheet, i, j)
			if _, ok := bodyFreq[sig]; !ok {
				bodySigOrder = append(bodySigOrder, sig)
			}
			bodyFreq[sig]++
		}
	}

	var bodySig fontSig
	bodySigSet := false
	bestCount := 0
	for _, sig := range bodySigOrder {
		if bodyFreq[sig] > bestCount {
			bestCount = bodyFreq[sig]
			bodySig = sig
			bodySigSet = true
		}
	}

	if !bodySigSet || bodyNonEmpty < 3 {
		return 1
	}

	headerRows := 0
	for i := 0; i < scanTop; i++ {
		if i >= len(matrix) {
			continue
		}
		nonEmpty, styleDiff, numeric := 0, 0, 0
		for j := 0; j <= maxCol && j < len(matrix[i]); j++ {
			v := matrix[i][j]
			if v == "" {
				continue
			}
			nonEmpty++
			if isNumericNoComma(v) {
				numeric++
			}
			sig := getFontSig(f, sheet, i, j)
			if sig != bodySig || sig.bold || sig.size > bodySig.size {
				styleDiff++
			}
		}
		if nonEmpty == 0 {
			continue
		}
		diffRatio := float64(styleDiff) / float64(nonEmpty)
		numericRatio := float64(numeric) / float64(nonEmpty)
		looksHeader := diffRatio >= 0.55 && numericRatio <= 0.5
		if looksHeader {
			headerRows = i + 1
			continue
		}
		if headerRows > 0 {
			break
		}
	}

	if headerRows <= 0 {
		return 1
	}
	return headerRows
}

// detectRowDimensionColumns mirrors detectRowDimensionColumns: a contiguous
// prefix of columns starting at 0, each mostly non-numeric text.
func detectRowDimensionColumns(matrix [][]string, headerRows, maxRow, maxCol int) []int {
	startRow := maxInt(0, headerRows)
	var dims []int
	for c := 0; c <= maxCol; c++ {
		nonEmpty, nonNumeric := 0, 0
		for r := startRow; r <= maxRow; r++ {
			if r < 0 || r >= len(matrix) || c >= len(matrix[r]) {
				continue
			}
			v := matrix[r][c]
			if v == "" {
				continue
			}
			nonEmpty++
			if !isNumeric(v) {
				nonNumeric++
			}
		}
		if nonEmpty >= 3 && nonNumeric >= maxInt(2, int(float64(nonEmpty)*0.6)) {
			dims = append(dims, c)
		} else if len(dims) > 0 {
			break
		}
	}
	return dims
}

// detectHeaderFieldNameForColumn mirrors detectHeaderFieldNameForColumn.
func detectHeaderFieldNameForColumn(matrix [][]string, headerRows, col int) string {
	limit := minInt(headerRows, len(matrix))
	for i := limit - 1; i >= 0; i-- {
		if col < len(matrix[i]) {
			v := matrix[i][col]
			if v != "" && !isNumeric(v) {
				return v
			}
		}
	}
	for i := 0; i < limit; i++ {
		if col < len(matrix[i]) {
			v := matrix[i][col]
			if v != "" && !isNumeric(v) {
				return v
			}
		}
	}
	return "dim_" + strconv.Itoa(col)
}

type constantHeaderLabel struct {
	row   int
	value string
}

// detectConstantHeaderLabels mirrors detectConstantHeaderLabels: header rows
// where one value dominates (ratio>=0.8, count>=2) among non-empty,
// non-numeric cells in [fromCol,toCol].
func detectConstantHeaderLabels(matrix [][]string, headerRows, fromCol, toCol int) []constantHeaderLabel {
	rows := minInt(headerRows, len(matrix))
	var result []constantHeaderLabel
	if rows <= 0 {
		return result
	}
	for i := 0; i < rows; i++ {
		freq := newOrderedStringFreq()
		candidates := 0
		for j := fromCol; j <= toCol; j++ {
			if j < 0 || j >= len(matrix[i]) {
				continue
			}
			v := matrix[i][j]
			if v == "" || isNumeric(v) {
				continue
			}
			freq.add(v)
			candidates++
		}
		if candidates <= 0 {
			continue
		}
		bestVal, bestCount := freq.best()
		ratio := float64(bestCount) / float64(candidates)
		if ratio >= 0.8 && bestCount >= 2 {
			result = append(result, constantHeaderLabel{row: i, value: bestVal})
		}
	}
	return result
}

// detectFirstNonEmptyHeaderCell mirrors detectFirstNonEmptyHeaderCell.
func detectFirstNonEmptyHeaderCell(matrix [][]string, headerRows, fromCol, toCol int) string {
	limit := minInt(headerRows, len(matrix))
	for i := 0; i < limit; i++ {
		for j := fromCol; j <= toCol; j++ {
			if j < 0 || j >= len(matrix[i]) {
				continue
			}
			v := matrix[i][j]
			if v != "" && !isNumeric(v) {
				return v
			}
		}
	}
	return "col_dim"
}

// detectMostFrequentHeaderCell mirrors detectMostFrequentHeaderCell.
func detectMostFrequentHeaderCell(matrix [][]string, headerRows, fromCol, toCol int, exclude1 string) string {
	rows := minInt(headerRows, len(matrix))
	freq := newOrderedStringFreq()
	candidates := 0
	for i := 0; i < rows; i++ {
		for j := fromCol; j <= toCol; j++ {
			if j < 0 || j >= len(matrix[i]) {
				continue
			}
			v := matrix[i][j]
			if v == "" || isNumeric(v) || v == exclude1 {
				continue
			}
			freq.add(v)
			candidates++
		}
	}
	if candidates <= 0 || freq.len() == 0 {
		return "value"
	}
	bestVal, bestCount := freq.best()
	ratio := float64(bestCount) / float64(candidates)
	if ratio >= 0.3 {
		return bestVal
	}
	return "value"
}

// buildFallbackColDimension mirrors buildFallbackColDimension: concatenate
// all header-row cells for the column (no exclusion of constant label rows).
func buildFallbackColDimension(matrix [][]string, headerRows, col int) string {
	rows := minInt(headerRows, len(matrix))
	var parts []string
	for i := 0; i < rows; i++ {
		if col < len(matrix[i]) && matrix[i][col] != "" {
			parts = append(parts, matrix[i][col])
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "_")
	}
	return "col_" + strconv.Itoa(col)
}

var (
	reCategoryQ      = regexp.MustCompile(`(?i)^q\d+$`)
	reCategoryMonth  = regexp.MustCompile(`^\d{1,2}月$`)
	reCategoryYear   = regexp.MustCompile(`^\d{4}年$`)
	reCategoryDigits = regexp.MustCompile(`^\d+$`)
)

// isCategoryLikeHeader mirrors isCategoryLikeHeader.
func isCategoryLikeHeader(h string) bool {
	h = strings.TrimSpace(h)
	if reCategoryQ.MatchString(h) || reCategoryMonth.MatchString(h) || reCategoryYear.MatchString(h) || reCategoryDigits.MatchString(h) {
		return true
	}
	if strings.Contains(h, "季度") || strings.Contains(h, "月") || strings.Contains(h, "年") {
		return true
	}
	return false
}

// looksLikePivotSingleHeader mirrors looksLikePivotSingleHeader.
func looksLikePivotSingleHeader(matrix [][]string, headerRows, maxRow, fromCol, toCol int) bool {
	if headerRows != 1 {
		return false
	}
	valueCols := maxInt(0, toCol-fromCol+1)
	if valueCols < 3 {
		return false
	}
	nonEmptyHeaders, categoryLike := 0, 0
	if len(matrix) > 0 {
		for j := fromCol; j <= toCol; j++ {
			if j < 0 || j >= len(matrix[0]) {
				continue
			}
			v := matrix[0][j]
			if v == "" {
				continue
			}
			nonEmptyHeaders++
			if isCategoryLikeHeader(v) {
				categoryLike++
			}
		}
	}
	if nonEmptyHeaders == 0 {
		return false
	}
	hasNumbers := hasEnoughNumericValues(matrix, headerRows, maxRow, fromCol, toCol)
	return hasNumbers && categoryLike >= maxInt(2, nonEmptyHeaders/2)
}
