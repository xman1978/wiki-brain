// Package localconvert implements a pure-Go, in-process fallback for the
// remote FileView conversion service (docs/impl/v1/local-file-convert.md).
// excel.go is a logic-to-logic port of FileView's ExcelToMarkdown.java
// (docs/impl/v1/xlsx-port/01-excel-to-markdown.md): every threshold and
// decision branch is copied as-is, only the Aspose Cells calls are swapped
// for excelize equivalents.
package localconvert

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ConvertExcelToMarkdown mirrors ExcelToMarkdown.convert(input, output).
func ConvertExcelToMarkdown(srcPath string) ([]byte, error) {
	f, err := excelize.OpenFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: open excel: %w", err)
	}
	defer f.Close()

	var sheetRoots []jsonObj
	for _, sheet := range f.GetSheetList() {
		matrix, maxRow, maxCol, err := buildSheetMatrix(f, sheet)
		if err != nil {
			return nil, fmt.Errorf("localconvert: read sheet %q: %w", sheet, err)
		}
		if maxRow < 0 && maxCol < 0 {
			continue
		}
		root := buildPivotJSONForSheet(f, sheet, matrix, maxRow, maxCol)
		sheetRoots = append(sheetRoots, root)
	}

	if len(sheetRoots) == 0 {
		fileName := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
		sheetRoots = append(sheetRoots, jsonObj{}.
			set("table_name", fileName).
			set("schema", jsonArr{}).
			set("data", jsonArr{}))
	}

	multiSheet := len(sheetRoots) > 1

	var b strings.Builder
	for i, root := range sheetRoots {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if multiSheet {
			tableName := ""
			for _, f := range root {
				if f.key == "table_name" {
					if s, ok := f.val.(string); ok {
						tableName = s
					}
				}
			}
			b.WriteString("## Sheet: ")
			b.WriteString(tableName)
			b.WriteString("\n\n")
		}
		b.WriteString("```json\n")
		b.WriteString(MarshalJackson(root))
		b.WriteString("\n```")

		var data jsonArr
		tableName := ""
		for _, kv := range root {
			switch kv.key {
			case "data":
				if arr, ok := kv.val.(jsonArr); ok {
					data = arr
				}
			case "table_name":
				if s, ok := kv.val.(string); ok {
					tableName = s
				}
			}
		}
		statements := buildRowLevelStatements(tableName, data)
		if statements != "" {
			b.WriteString("\n```text\n")
			b.WriteString(statements)
			b.WriteString("\n```")
		}
	}

	return []byte(b.String()), nil
}

// buildSheetMatrix reads a sheet into a 0-based [row][col] string matrix,
// determines maxRow/maxCol (Aspose getMaxDataRow/getMaxDataColumn equivalent,
// see xlsx-port/01 §13), and physically expands merged cells over it
// (xlsx-port/01 §12).
func buildSheetMatrix(f *excelize.File, sheet string) ([][]string, int, int, error) {
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, -1, -1, err
	}
	if len(rows) == 0 {
		return nil, -1, -1, nil
	}

	maxCol := -1
	for _, row := range rows {
		if len(row)-1 > maxCol {
			maxCol = len(row) - 1
		}
	}
	maxRow := len(rows) - 1
	if maxCol < 0 {
		return nil, -1, -1, nil
	}

	matrix := make([][]string, maxRow+1)
	for r := 0; r <= maxRow; r++ {
		matrix[r] = make([]string, maxCol+1)
		for c := 0; c <= maxCol; c++ {
			matrix[r][c] = safeCellString(f, sheet, r, c)
		}
	}

	merges, err := f.GetMergeCells(sheet)
	if err != nil {
		return nil, -1, -1, err
	}
	for _, mc := range merges {
		startCol, startRow, err := excelize.CellNameToCoordinates(mc.GetStartAxis())
		if err != nil {
			continue
		}
		endCol, endRow, err := excelize.CellNameToCoordinates(mc.GetEndAxis())
		if err != nil {
			continue
		}
		// excelize coordinates are 1-based; matrix is 0-based.
		sr, sc, er, ec := startRow-1, startCol-1, endRow-1, endCol-1
		if sr < 0 || sc < 0 || sr > maxRow || sc > maxCol {
			continue
		}
		value := matrix[sr][sc]
		for i := sr; i <= er && i <= maxRow; i++ {
			for j := sc; j <= ec && j <= maxCol; j++ {
				matrix[i][j] = value
			}
		}
	}

	return matrix, maxRow, maxCol, nil
}

// safeCellString mirrors safeCellString: any read error yields "".
func safeCellString(f *excelize.File, sheet string, row, col int) string {
	cellRef, err := excelize.CoordinatesToCellName(col+1, row+1)
	if err != nil {
		return ""
	}
	v, err := f.GetCellValue(sheet, cellRef)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// buildPivotJSONForSheet mirrors buildPivotJsonForSheet: the core decision
// chain deciding pivot vs. table mode (xlsx-port/01 §2). The four
// degrade-to-table checks are evaluated in fixed order and short-circuit.
func buildPivotJSONForSheet(f *excelize.File, sheet string, matrix [][]string, maxRow, maxCol int) jsonObj {
	tableName := sheet

	if maxRow < 0 || maxCol < 0 {
		return jsonObj{}.
			set("table_name", tableName).
			set("data", jsonArr{}).
			set("meta", jsonObj{}.set("sheet", sheet).set("is_pivot", true))
	}

	headerRows := detectHeaderRowsWithStyle(f, sheet, matrix, maxRow, maxCol)

	rowDimCols := detectRowDimensionColumns(matrix, headerRows, maxRow, maxCol)
	if len(rowDimCols) == 0 {
		rowDimCols = []int{0}
	}
	rowDimFieldNames := make([]string, len(rowDimCols))
	for i, c := range rowDimCols {
		rowDimFieldNames[i] = detectHeaderFieldNameForColumn(matrix, headerRows, c)
	}
	valueStartCol := rowDimCols[len(rowDimCols)-1] + 1

	// Degrade 1: dimension columns cover the entire sheet.
	if valueStartCol > maxCol {
		return buildTableJSONForSheet(matrix, headerRows, maxRow, maxCol, tableName)
	}

	constantHeaderLabels := detectConstantHeaderLabels(matrix, headerRows, valueStartCol, maxCol)
	var colDimFieldName, measureFieldName string
	if len(constantHeaderLabels) > 0 {
		colDimFieldName = constantHeaderLabels[0].value
	} else {
		colDimFieldName = detectFirstNonEmptyHeaderCell(matrix, headerRows, valueStartCol, maxCol)
	}
	if len(constantHeaderLabels) > 1 {
		measureFieldName = constantHeaderLabels[1].value
	} else {
		measureFieldName = detectMostFrequentHeaderCell(matrix, headerRows, valueStartCol, maxCol, colDimFieldName)
	}

	// Degrade 2: single header row, no constant labels, doesn't look pivot.
	if headerRows <= 1 && len(constantHeaderLabels) == 0 && !looksLikePivotSingleHeader(matrix, headerRows, maxRow, valueStartCol, maxCol) {
		return buildTableJSONForSheet(matrix, headerRows, maxRow, maxCol, tableName)
	}

	profilesAll := profileColumns(matrix, headerRows, maxRow, maxCol)
	mostlyNumeric, mostlyText := 0, 0
	for c := valueStartCol; c <= maxCol; c++ {
		if profilesAll[c].isMostlyNumeric() {
			mostlyNumeric++
		}
		if profilesAll[c].isMostlyText() {
			mostlyText++
		}
	}

	// Degrade 3: value area has both a dominant numeric column and a
	// dominant text column.
	if mostlyNumeric >= 1 && mostlyText >= 1 {
		return buildTableJSONForSheet(matrix, headerRows, maxRow, maxCol, tableName)
	}

	// Degrade 4: not enough numeric values in the value area.
	if !hasEnoughNumericValues(matrix, headerRows, maxRow, valueStartCol, maxCol) {
		return buildTableJSONForSheet(matrix, headerRows, maxRow, maxCol, tableName)
	}

	schema := jsonArr{jsonObj{}.set("name", "id").set("type", "string")}
	for _, name := range rowDimFieldNames {
		schema = append(schema, jsonObj{}.set("name", name).set("type", "string"))
	}
	schema = append(schema, jsonObj{}.set("name", colDimFieldName).set("type", "string"))
	schema = append(schema, jsonObj{}.set("name", measureFieldName).set("type", "number"))

	excludedRows := map[int]bool{}
	for _, l := range constantHeaderLabels {
		excludedRows[l.row] = true
	}
	colDimensions := make([]string, maxCol-valueStartCol+1)
	for j := valueStartCol; j <= maxCol; j++ {
		var parts []string
		for i := 0; i < headerRows; i++ {
			if excludedRows[i] {
				continue
			}
			if i < len(matrix) && j < len(matrix[i]) && matrix[i][j] != "" {
				parts = append(parts, matrix[i][j])
			}
		}
		joined := strings.Join(parts, "_")
		if joined == "" {
			joined = buildFallbackColDimension(matrix, headerRows, j)
		}
		colDimensions[j-valueStartCol] = joined
	}

	lastDimVals := make([]string, len(rowDimCols))
	hasDimVal := make([]bool, len(rowDimCols))

	data := jsonArr{}
	for i := headerRows; i <= maxRow; i++ {
		dimVals := make([]string, len(rowDimCols))
		dimValSet := make([]bool, len(rowDimCols))
		for idx, col := range rowDimCols {
			v := ""
			if i < len(matrix) && col < len(matrix[i]) {
				v = matrix[i][col]
			}
			if v != "" {
				if !hasDimVal[idx] || v != lastDimVals[idx] {
					for k := idx + 1; k < len(rowDimCols); k++ {
						hasDimVal[k] = false
						lastDimVals[k] = ""
					}
				}
				lastDimVals[idx] = v
				hasDimVal[idx] = true
				dimVals[idx] = v
				dimValSet[idx] = true
			} else if hasDimVal[idx] {
				dimVals[idx] = lastDimVals[idx]
				dimValSet[idx] = true
			}
		}
		if !dimValSet[0] || dimVals[0] == "" {
			continue
		}

		for j := valueStartCol; j <= maxCol; j++ {
			value := ""
			if i < len(matrix) && j < len(matrix[i]) {
				value = matrix[i][j]
			}
			if value == "" {
				continue
			}
			record := jsonObj{}.set("id", excelRowID(i))
			for idx, name := range rowDimFieldNames {
				v := ""
				if dimValSet[idx] {
					v = dimVals[idx]
				}
				record = record.set(name, v)
			}
			record = record.set(colDimFieldName, colDimensions[j-valueStartCol])
			record = record.set(measureFieldName, jsonDouble(parseNumber(value)))
			data = append(data, record)
		}
	}

	meta := jsonObj{}.
		set("source", "excel").
		set("sheet", sheet).
		set("is_pivot", true)

	return jsonObj{}.
		set("table_name", tableName).
		set("schema", schema).
		set("data", data).
		set("meta", meta)
}

// hasEnoughNumericValues mirrors hasEnoughNumericValues (comma-stripped
// numeric check, one of the three call sites that strips).
func hasEnoughNumericValues(matrix [][]string, headerRows, maxRow, fromCol, toCol int) bool {
	from := maxInt(0, fromCol)
	numeric, nonEmpty := 0, 0
	for r := headerRows; r <= maxRow; r++ {
		if r < 0 || r >= len(matrix) {
			continue
		}
		for c := from; c <= toCol; c++ {
			if c < 0 || c >= len(matrix[r]) {
				continue
			}
			v := matrix[r][c]
			if v == "" {
				continue
			}
			nonEmpty++
			if isNumericNoComma(v) {
				numeric++
			}
		}
	}
	if nonEmpty == 0 {
		return false
	}
	return numeric >= 3 && float64(numeric)/float64(nonEmpty) >= 0.2
}
