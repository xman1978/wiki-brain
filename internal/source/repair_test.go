package source

import (
	"testing"
)

func TestRepairSections_ClampLineRange(t *testing.T) {
	sections := []llmSection{
		{Title: "A", Summary: "k", LineStart: -5, LineEnd: 100, Level: 1},
	}
	result := repairSections(sections, 50)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].LineStart != 1 || result[0].LineEnd != 50 {
		t.Errorf("range = %d-%d, want 1-50", result[0].LineStart, result[0].LineEnd)
	}
}

func TestRepairSections_DropInvalidStartEnd(t *testing.T) {
	sections := []llmSection{
		{Title: "Good", Summary: "k", LineStart: 1, LineEnd: 10, Level: 1},
		{Title: "Bad", Summary: "k", LineStart: 20, LineEnd: 5, Level: 1},
	}
	result := repairSections(sections, 30)
	if len(result) != 1 {
		t.Errorf("len = %d, want 1 (bad section should be dropped)", len(result))
	}
}

func TestRepairSections_ClampLevel(t *testing.T) {
	sections := []llmSection{
		{Title: "A", Summary: "k", LineStart: 1, LineEnd: 10, Level: 0},
		{Title: "B", Summary: "k", LineStart: 11, LineEnd: 20, Level: 5},
	}
	result := repairSections(sections, 20)
	if result[0].Level != 1 {
		t.Errorf("level[0] = %d, want 1", result[0].Level)
	}
	if result[1].Level != 3 {
		t.Errorf("level[1] = %d, want 3", result[1].Level)
	}
}

func TestRepairSections_DropEmptyTitle(t *testing.T) {
	sections := []llmSection{
		{Title: "", Summary: "k", LineStart: 1, LineEnd: 10, Level: 1},
		{Title: "  ", Summary: "k", LineStart: 11, LineEnd: 20, Level: 1},
		{Title: "Good", Summary: "k", LineStart: 1, LineEnd: 20, Level: 1},
	}
	result := repairSections(sections, 20)
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
}

func TestRepairSections_FixOverlap(t *testing.T) {
	sections := []llmSection{
		{Title: "A", Summary: "k", LineStart: 1, LineEnd: 15, Level: 1},
		{Title: "B", Summary: "k", LineStart: 10, LineEnd: 20, Level: 1},
	}
	result := repairSections(sections, 20)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	// A's line_end should be truncated to 9 (B.line_start - 1)
	if result[0].LineEnd != 9 {
		t.Errorf("A.LineEnd = %d, want 9", result[0].LineEnd)
	}
	if result[1].LineStart != 10 || result[1].LineEnd != 20 {
		t.Errorf("B = %d-%d, want 10-20", result[1].LineStart, result[1].LineEnd)
	}
}

func TestRepairSections_FillGap(t *testing.T) {
	sections := []llmSection{
		{Title: "A", Summary: "k", LineStart: 1, LineEnd: 5, Level: 1},
		{Title: "B", Summary: "k", LineStart: 10, LineEnd: 20, Level: 1},
	}
	result := repairSections(sections, 20)
	if len(result) != 2 {
		t.Fatalf("len = %d", len(result))
	}
	// A's line_end should be extended to 9 to fill the gap
	if result[0].LineEnd != 9 {
		t.Errorf("A.LineEnd = %d, want 9", result[0].LineEnd)
	}
}

func TestRepairSections_ExtendLastToEnd(t *testing.T) {
	sections := []llmSection{
		{Title: "A", Summary: "k", LineStart: 1, LineEnd: 10, Level: 1},
		{Title: "B", Summary: "k", LineStart: 11, LineEnd: 15, Level: 1},
	}
	result := repairSections(sections, 30)
	// Last top-level node should extend to totalLines
	if result[len(result)-1].LineEnd != 30 {
		t.Errorf("last.LineEnd = %d, want 30", result[len(result)-1].LineEnd)
	}
}

func TestRepairSections_SortByLineStart(t *testing.T) {
	sections := []llmSection{
		{Title: "B", Summary: "k", LineStart: 11, LineEnd: 20, Level: 1},
		{Title: "A", Summary: "k", LineStart: 1, LineEnd: 10, Level: 1},
	}
	result := repairSections(sections, 20)
	if result[0].Title != "A" || result[1].Title != "B" {
		t.Errorf("order: %s, %s — want A, B", result[0].Title, result[1].Title)
	}
}

func TestRepairSections_MixedLevelsNoOverlapFix(t *testing.T) {
	// L1 and L2 overlap is fine — only same-level overlap gets fixed
	sections := []llmSection{
		{Title: "Ch1", Summary: "k", LineStart: 1, LineEnd: 20, Level: 1},
		{Title: "Sec1", Summary: "k", LineStart: 1, LineEnd: 10, Level: 2},
		{Title: "Sec2", Summary: "k", LineStart: 11, LineEnd: 20, Level: 2},
	}
	result := repairSections(sections, 20)
	if len(result) != 3 {
		t.Fatalf("len = %d, want 3", len(result))
	}
	// Ch1 should keep its original range (L1 vs L2 don't conflict)
	if result[0].LineEnd != 20 {
		t.Errorf("Ch1.LineEnd = %d, want 20", result[0].LineEnd)
	}
}
