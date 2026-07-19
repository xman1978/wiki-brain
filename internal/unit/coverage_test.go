package unit

import "testing"

func TestComputeCoverage_FullyCovered(t *testing.T) {
	segments := []Segment{{Title: "第一节", LineStart: 1, LineEnd: 4}}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 4, Status: "completed"},
	}
	mdLines := []string{"a", "b", "c", "d"}

	report := ComputeCoverage(segments, units, mdLines)
	if len(report) != 1 {
		t.Fatalf("got %d reports, want 1", len(report))
	}
	if report[0].TotalLines != 4 || report[0].CoveredLines != 4 {
		t.Errorf("total=%d covered=%d, want 4,4", report[0].TotalLines, report[0].CoveredLines)
	}
	if len(report[0].Gaps) != 0 {
		t.Errorf("gaps = %v, want none", report[0].Gaps)
	}
}

func TestComputeCoverage_SegmentListItselfMissesLines(t *testing.T) {
	// Segments only cover L1-4 and L8-10 of a 10-line document — L5-7 was
	// never a segment at all (the Dock Swam-style failure: a segmentation
	// bug drops a range before ComputeCoverage ever sees it as something to
	// check). This must surface as its own gap entry instead of silently
	// reporting "fully covered" over the segments it did receive.
	segments := []Segment{
		{Title: "第一节", LineStart: 1, LineEnd: 4},
		{Title: "第二节", LineStart: 8, LineEnd: 10},
	}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 4, Status: "completed"},
		{LineStart: 8, LineEnd: 10, Status: "completed"},
	}
	mdLines := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}

	report := ComputeCoverage(segments, units, mdLines)
	if len(report) != 3 {
		t.Fatalf("got %d reports, want 3 (2 segments + 1 synthesized gap)", len(report))
	}

	last := report[2]
	if last.LineStart != 5 || last.LineEnd != 7 {
		t.Errorf("synthesized gap = L%d-L%d, want L5-L7", last.LineStart, last.LineEnd)
	}
	if last.TotalLines != 3 || last.CoveredLines != 0 {
		t.Errorf("synthesized gap total/covered = %d/%d, want 3/0", last.TotalLines, last.CoveredLines)
	}
	if len(last.Gaps) != 1 || last.Gaps[0].LineStart != 5 || last.Gaps[0].LineEnd != 7 {
		t.Errorf("synthesized gap's Gaps = %v, want one gap L5-L7", last.Gaps)
	}
}

func TestComputeCoverage_MiddleGap(t *testing.T) {
	segments := []Segment{{Title: "第一节", LineStart: 1, LineEnd: 6}}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 2, Status: "completed"},
		{LineStart: 5, LineEnd: 6, Status: "completed"},
	}
	mdLines := []string{"标题", "正文一", "过渡文字", "详见附件", "正文二", "结尾"}

	report := ComputeCoverage(segments, units, mdLines)
	if report[0].CoveredLines != 4 {
		t.Errorf("covered = %d, want 4", report[0].CoveredLines)
	}
	if len(report[0].Gaps) != 1 {
		t.Fatalf("got %d gaps, want 1", len(report[0].Gaps))
	}
	gap := report[0].Gaps[0]
	if gap.LineStart != 3 || gap.LineEnd != 4 {
		t.Errorf("gap = %d-%d, want 3-4", gap.LineStart, gap.LineEnd)
	}
	if gap.Preview != "过渡文字 详见附件" {
		t.Errorf("preview = %q, want %q", gap.Preview, "过渡文字 详见附件")
	}
}

func TestComputeCoverage_CumulativeUnitsFillSegment(t *testing.T) {
	segments := []Segment{{Title: "第一节", LineStart: 1, LineEnd: 6}}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 2, Status: "completed"},
		{LineStart: 3, LineEnd: 4, Status: "completed"},
		{LineStart: 5, LineEnd: 6, Status: "completed"},
	}
	mdLines := []string{"a", "b", "c", "d", "e", "f"}

	report := ComputeCoverage(segments, units, mdLines)
	if report[0].CoveredLines != 6 || len(report[0].Gaps) != 0 {
		t.Errorf("covered=%d gaps=%v, want 6 lines fully covered with no gaps", report[0].CoveredLines, report[0].Gaps)
	}
}

func TestComputeCoverage_ExtractionFailedFlaggedNotCounted(t *testing.T) {
	segments := []Segment{{Title: "第一节", LineStart: 1, LineEnd: 4}}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 4, Status: "extraction_failed"},
	}
	mdLines := []string{"a", "b", "c", "d"}

	report := ComputeCoverage(segments, units, mdLines)
	if report[0].CoveredLines != 0 {
		t.Errorf("covered = %d, want 0 (extraction_failed must not count as coverage)", report[0].CoveredLines)
	}
	if !report[0].HasExtractionFailed {
		t.Error("expected HasExtractionFailed = true")
	}
	if len(report[0].Gaps) != 1 || report[0].Gaps[0].LineStart != 1 || report[0].Gaps[0].LineEnd != 4 {
		t.Errorf("gaps = %v, want the whole segment flagged as a gap", report[0].Gaps)
	}
}

func TestComputeCoverage_NoUnitsAtAll(t *testing.T) {
	segments := []Segment{{Title: "第一节", LineStart: 1, LineEnd: 3}}
	mdLines := []string{"a", "b", "c"}

	report := ComputeCoverage(segments, nil, mdLines)
	if report[0].CoveredLines != 0 {
		t.Errorf("covered = %d, want 0", report[0].CoveredLines)
	}
	if len(report[0].Gaps) != 1 || report[0].Gaps[0].LineStart != 1 || report[0].Gaps[0].LineEnd != 3 {
		t.Errorf("gaps = %v, want the whole segment as one gap", report[0].Gaps)
	}
	if report[0].HasExtractionFailed {
		t.Error("no units at all is not the same as an extraction_failed placeholder")
	}
}

func TestComputeCoverage_UnitOverflowOnlyCreditsOverlap(t *testing.T) {
	// Pre-anchor-fix data could have a unit whose range spills past its own
	// segment (the exact a100c7a5 failure mode) — it must only be credited
	// for the lines it actually overlaps in a neighboring segment, not the
	// whole neighboring segment.
	segments := []Segment{
		{Title: "第一节", LineStart: 1, LineEnd: 2},
		{Title: "第二节", LineStart: 3, LineEnd: 5},
	}
	units := []KnowledgeUnit{
		{LineStart: 1, LineEnd: 3, Status: "completed"}, // 1 line into segment 2
	}
	mdLines := []string{"a", "b", "c", "d", "e"}

	report := ComputeCoverage(segments, units, mdLines)
	if report[0].CoveredLines != 2 {
		t.Errorf("segment 1 covered = %d, want 2", report[0].CoveredLines)
	}
	if report[1].CoveredLines != 1 {
		t.Errorf("segment 2 covered = %d, want 1 (only the overlapping line)", report[1].CoveredLines)
	}
	if len(report[1].Gaps) != 1 || report[1].Gaps[0].LineStart != 4 || report[1].Gaps[0].LineEnd != 5 {
		t.Errorf("segment 2 gaps = %v, want 4-5", report[1].Gaps)
	}
}
