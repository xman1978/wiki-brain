package unit

import (
	"strings"
	"testing"
)

func TestDetectNumericRowSignatures_TableSkipsHeaderAndSeparator(t *testing.T) {
	content := strings.Join([]string{
		"| 积分项目 | 细项 | 分值 |",
		"| --- | --- | --- |",
		"| 出勤分 | 全勤 | +1 |",
		"| 结果分 | 考试成绩低于80分（含80分） | -1 |",
		"| 结果分 | 不参加考核 | -3 |",
	}, "\n")

	rows := detectNumericRowSignatures(content)
	if len(rows) != 3 {
		t.Fatalf("want 3 data rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].numbers[0] != "1" {
		t.Errorf("row0 numbers = %v, want [1]", rows[0].numbers)
	}
	if !contains(rows[1].numbers, "80") || !contains(rows[1].numbers, "1") {
		t.Errorf("row1 numbers = %v, want to contain 80 and 1", rows[1].numbers)
	}
}

func TestDetectNumericRowSignatures_ListItems(t *testing.T) {
	content := strings.Join([]string{
		"积分规则：",
		"1. 全勤得1分",
		"2. 旷课每次扣5分",
		"- 迟到早退累计2次扣1分",
		"- 无信息量的一项",
	}, "\n")

	rows := detectNumericRowSignatures(content)
	if len(rows) != 3 {
		t.Fatalf("want 3 numeric rows (last item has no number), got %d: %+v", len(rows), rows)
	}
}

func TestUncoveredRows_DetectsDroppedNumber(t *testing.T) {
	rows := []rowSignature{
		{text: "| 结果分 | 考试成绩低于80分（含80分） | -1 |", numbers: []string{"80", "1"}},
	}
	points := []llmPoint{
		{Content: "结果分考核培训产出与掌握程度，未达标或未参加则相应扣分。"},
	}
	missing := uncoveredRows(rows, points)
	if len(missing) != 1 {
		t.Fatalf("want 1 uncovered row, got %d", len(missing))
	}
}

func TestUncoveredRows_AllNumbersPresentIsCovered(t *testing.T) {
	rows := []rowSignature{
		{text: "| 结果分 | 考试成绩低于80分（含80分） | -1 |", numbers: []string{"80", "1"}},
	}
	points := []llmPoint{
		{Content: "考试成绩低于80分（含80分）扣1分；不参加考核扣3分。"},
	}
	missing := uncoveredRows(rows, points)
	if len(missing) != 0 {
		t.Fatalf("want 0 uncovered rows, got %d: %+v", len(missing), missing)
	}
}

func TestUncoveredRows_PartialNumberMatchStillUncovered(t *testing.T) {
	// Row needs both "80" and "1" present; content only has "1" (from a
	// different fact) — must not be marked covered by coincidence.
	rows := []rowSignature{
		{text: "| 结果分 | 考试成绩低于80分（含80分） | -1 |", numbers: []string{"80", "1"}},
	}
	points := []llmPoint{
		{Content: "出勤分全勤得1分。"},
	}
	missing := uncoveredRows(rows, points)
	if len(missing) != 1 {
		t.Fatalf("want 1 uncovered row (only coincidental partial match), got %d", len(missing))
	}
}

func TestUncoveredRows_NoNumericRowsNoop(t *testing.T) {
	if got := uncoveredRows(nil, []llmPoint{{Content: "无数字摘要"}}); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestCleanRowText(t *testing.T) {
	got := cleanRowText("| 结果分 | 考试成绩低于80分（含80分） | -1 |")
	want := "结果分，考试成绩低于80分（含80分），-1"
	if got != want {
		t.Errorf("cleanRowText table row = %q, want %q", got, want)
	}

	got = cleanRowText("1. 全勤得1分")
	want = "全勤得1分"
	if got != want {
		t.Errorf("cleanRowText list item = %q, want %q", got, want)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
