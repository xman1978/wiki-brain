package localconvert

import (
	"strconv"
	"strings"
)

type columnProfile struct {
	nonEmpty   int
	numeric    int
	nonNumeric int
}

// isMostlyNumeric mirrors ColumnProfile.isMostlyNumeric.
func (p columnProfile) isMostlyNumeric() bool {
	if p.nonEmpty == 0 {
		return false
	}
	return p.numeric >= maxInt(2, int(float64(p.nonEmpty)*0.6))
}

// isMostlyText mirrors ColumnProfile.isMostlyText. Can be simultaneously
// false alongside isMostlyNumeric for a column that is neither mostly
// numeric nor mostly text (see xlsx-port/01 §8.1).
func (p columnProfile) isMostlyText() bool {
	if p.nonEmpty == 0 {
		return false
	}
	return p.nonNumeric >= maxInt(2, int(float64(p.nonEmpty)*0.6))
}

// profileColumns mirrors profileColumns: numeric check strips commas first
// (one of the three call sites that does), and the per-row scan breaks (not
// continues) once a row is too short for column c — matching Java's
// `if (c >= matrix[r].length) break;`.
func profileColumns(matrix [][]string, headerRows, maxRow, maxCol int) []columnProfile {
	profiles := make([]columnProfile, maxCol+1)
	for c := 0; c <= maxCol; c++ {
		var p columnProfile
		for r := headerRows; r <= maxRow; r++ {
			if r < 0 || r >= len(matrix) {
				continue
			}
			if c >= len(matrix[r]) {
				break
			}
			v := matrix[r][c]
			if v == "" {
				continue
			}
			p.nonEmpty++
			if isNumericNoComma(v) {
				p.numeric++
			} else {
				p.nonNumeric++
			}
		}
		profiles[c] = p
	}
	return profiles
}

// detectCarryForwardColumns mirrors detectCarryForwardColumns.
func detectCarryForwardColumns(profiles []columnProfile) []bool {
	carry := make([]bool, len(profiles))
	for i, p := range profiles {
		carry[i] = p.isMostlyText()
	}
	return carry
}

// detectTableHeaders mirrors detectTableHeaders: concatenate header-row
// cells per column with "_", fall back to "col_<c>", then dedup with
// "_2"/"_3"... suffixes on repeats (first occurrence unsuffixed).
func detectTableHeaders(matrix [][]string, headerRows, maxCol int) []string {
	rows := minInt(headerRows, len(matrix))
	raw := make([]string, maxCol+1)
	for c := 0; c <= maxCol; c++ {
		var parts []string
		for i := 0; i < rows; i++ {
			if c < len(matrix[i]) && matrix[i][c] != "" {
				parts = append(parts, matrix[i][c])
			}
		}
		if len(parts) > 0 {
			raw[c] = strings.Join(parts, "_")
		} else {
			raw[c] = "col_" + strconv.Itoa(c)
		}
	}
	seen := map[string]int{}
	out := make([]string, len(raw))
	for i, name := range raw {
		seen[name]++
		if seen[name] == 1 {
			out[i] = name
		} else {
			out[i] = name + "_" + strconv.Itoa(seen[name])
		}
	}
	return out
}

// buildTableJSONForSheet mirrors buildTableJsonForSheet.
func buildTableJSONForSheet(matrix [][]string, headerRows, maxRow, maxCol int, tableName string) jsonObj {
	headers := detectTableHeaders(matrix, headerRows, maxCol)
	profiles := profileColumns(matrix, headerRows, maxRow, maxCol)
	carryForwardCols := detectCarryForwardColumns(profiles)

	schema := jsonArr{}
	schema = append(schema, jsonObj{}.set("name", "id").set("type", "string"))
	for c := 0; c <= maxCol; c++ {
		typ := "string"
		if profiles[c].isMostlyNumeric() {
			typ = "number"
		}
		schema = append(schema, jsonObj{}.set("name", headers[c]).set("type", typ))
	}

	lastVals := make([]string, maxCol+1)
	hasLastVal := make([]bool, maxCol+1)

	data := jsonArr{}
	for r := headerRows; r <= maxRow; r++ {
		row := jsonObj{}.set("id", excelRowID(r))
		anyNonEmpty := false
		for c := 0; c <= maxCol; c++ {
			v := ""
			if r < len(matrix) && c < len(matrix[r]) {
				v = matrix[r][c]
			}
			if carryForwardCols[c] {
				if v != "" {
					if !hasLastVal[c] || v != lastVals[c] {
						for k := c + 1; k <= maxCol; k++ {
							if carryForwardCols[k] {
								hasLastVal[k] = false
								lastVals[k] = ""
							}
						}
					}
					lastVals[c] = v
					hasLastVal[c] = true
				} else if hasLastVal[c] {
					v = lastVals[c]
				}
			}
			outVal := v
			if outVal != "" {
				anyNonEmpty = true
			}
			if profiles[c].isMostlyNumeric() {
				if outVal == "" {
					row = row.set(headers[c], nil)
				} else {
					row = row.set(headers[c], jsonDouble(parseNumber(outVal)))
				}
			} else {
				row = row.set(headers[c], outVal)
			}
		}
		if anyNonEmpty {
			data = append(data, row)
		}
	}

	meta := jsonObj{}.
		set("source", "excel").
		set("sheet", tableName).
		set("mode", "table").
		set("header_rows", headerRows)

	return jsonObj{}.
		set("table_name", tableName).
		set("schema", schema).
		set("data", data).
		set("meta", meta)
}
