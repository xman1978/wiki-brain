package localconvert

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// orderedStringFreq counts string frequencies while preserving first-seen
// order, so "most frequent, ties broken by first occurrence" is
// deterministic (a plain Go map has randomized iteration order).
type orderedStringFreq struct {
	order []string
	count map[string]int
}

func newOrderedStringFreq() *orderedStringFreq {
	return &orderedStringFreq{count: map[string]int{}}
}

func (f *orderedStringFreq) add(v string) {
	if _, ok := f.count[v]; !ok {
		f.order = append(f.order, v)
	}
	f.count[v]++
}

func (f *orderedStringFreq) len() int {
	return len(f.order)
}

// best returns the value with the highest count, first occurrence wins ties.
func (f *orderedStringFreq) best() (string, int) {
	bestVal, bestCount := "", 0
	for _, v := range f.order {
		if f.count[v] > bestCount {
			bestCount = f.count[v]
			bestVal = v
		}
	}
	return bestVal, bestCount
}

var reNumeric = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// isNumeric mirrors ExcelToMarkdown.java's isNumeric(str): a plain
// integer/decimal string, no scientific notation, no percent, no comma.
func isNumeric(s string) bool {
	return reNumeric.MatchString(s)
}

// isNumericNoComma is isNumeric applied after stripping thousands separators.
// Only used at the three call sites (detectHeaderRowsByFontStyle,
// profileColumns, hasEnoughNumericValues) that strip commas before checking —
// every other call site in the Java source passes the raw value, and that
// inconsistency is preserved deliberately (see xlsx-port/01 §10.1).
func isNumericNoComma(s string) bool {
	return isNumeric(strings.ReplaceAll(s, ",", ""))
}

// parseNumber mirrors ExcelToMarkdown.java's parseNumber: strip commas, parse
// as double; any failure (including non-numeric input) silently yields 0.
func parseNumber(s string) float64 {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil {
		return 0
	}
	return v
}

// excelRowID mirrors excelRowId: 1-based row number, zero-padded to 4 digits
// up to 9999, unpadded beyond that.
func excelRowID(zeroBasedRow int) string {
	row := zeroBasedRow + 1
	if row < 1 {
		row = 1
	}
	if row <= 9999 {
		return fmt.Sprintf("row_%04d", row)
	}
	return fmt.Sprintf("row_%d", row)
}

// buildRowLevelStatements mirrors buildRowLevelStatements: one
// "id=... | 表=... | data={...}" line per data record, compact JSON.
func buildRowLevelStatements(tableName string, data jsonArr) string {
	if len(data) == 0 {
		return ""
	}
	lines := make([]string, 0, len(data))
	for _, e := range data {
		row, ok := e.(jsonObj)
		if !ok {
			continue
		}
		rowID := ""
		for _, f := range row {
			if f.key == "id" {
				if s, ok := f.val.(string); ok {
					rowID = s
				}
			}
		}
		lines = append(lines, fmt.Sprintf("id=%s | 表=%s | data=%s", rowID, tableName, marshalCompact(row)))
	}
	return strings.Join(lines, "\n")
}

// marshalCompact renders a jsonObj as compact (non-pretty) JSON, preserving
// field order, for the row-level statement lines.
func marshalCompact(o jsonObj) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, f.key)
		b.WriteByte(':')
		writeCompactValue(&b, f.val)
	}
	b.WriteByte('}')
	return b.String()
}

func writeCompactValue(b *strings.Builder, v interface{}) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		writeJSONString(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case jsonDouble:
		b.WriteString(formatJavaDouble(float64(x)))
	case jsonObj:
		b.WriteByte('{')
		for i, f := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, f.key)
			b.WriteByte(':')
			writeCompactValue(b, f.val)
		}
		b.WriteByte('}')
	case jsonArr:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCompactValue(b, e)
		}
		b.WriteByte(']')
	default:
		b.WriteString("null")
	}
}
