package unit

// CoverageGap is a contiguous run of lines within a segment that no
// status=completed KnowledgeUnit covers.
type CoverageGap struct {
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Preview   string `json:"preview"`
}

// SegmentCoverage reports, for one extraction segment, how much of its line
// range ended up inside a completed KnowledgeUnit.
type SegmentCoverage struct {
	OutlineTitle        string        `json:"outline_title"`
	LineStart           int           `json:"line_start"`
	LineEnd             int           `json:"line_end"`
	TotalLines          int           `json:"total_lines"`
	CoveredLines        int           `json:"covered_lines"`
	Gaps                []CoverageGap `json:"gaps"`
	HasExtractionFailed bool          `json:"has_extraction_failed"`
}

const gapPreviewMaxRunes = 60

// ComputeCoverage reports, per segment, which of its lines are not covered
// by any status=completed unit. Only completed units count as coverage —
// an extraction_failed placeholder's line range degenerates to the whole
// segment (see insertFailedUnit), which would otherwise mask a real gap, so
// its presence is surfaced separately via HasExtractionFailed instead.
//
// This also independently diffs the segment list itself against the full
// document (mdLines) and surfaces any line range no segment covers at all —
// it does not just trust that BuildSegments produced full coverage. Before
// 2026-07-16's uncoveredSegments fix, a misdetected heading could make
// BuildSegments silently drop ~63% of a document's lines from the segment
// list entirely (Dock Swam 集群部署: segments summed to 74 of 203 lines),
// and this function had no way to notice — every segment it did receive
// looked 100% covered, so the report read "no gaps" while most of the
// document was invisible to it. Diffing against mdLines here means a
// regression in segmentation surfaces as a gap instead of silently
// vanishing again, independent of whatever BuildSegments currently does.
func ComputeCoverage(segments []Segment, units []KnowledgeUnit, mdLines []string) []SegmentCoverage {
	reports := make([]SegmentCoverage, 0, len(segments)+1)

	for _, seg := range segments {
		total := seg.LineEnd - seg.LineStart + 1
		if total < 0 {
			total = 0
		}
		covered := make([]bool, total)
		hasFailed := false

		for _, u := range units {
			if u.Status == "extraction_failed" {
				if u.LineStart <= seg.LineEnd && u.LineEnd >= seg.LineStart {
					hasFailed = true
				}
				continue
			}
			if u.Status != "completed" {
				continue
			}
			// Intersect with the segment's own bounds so a unit whose range
			// spills outside this segment (old, pre-anchor-fix data) can't
			// mark lines covered that aren't actually this segment's.
			from := max(u.LineStart, seg.LineStart)
			to := min(u.LineEnd, seg.LineEnd)
			for line := from; line <= to; line++ {
				covered[line-seg.LineStart] = true
			}
		}

		coveredCount := 0
		var gaps []CoverageGap
		gapStart := -1
		for i := 0; i <= total; i++ {
			isCovered := i < total && covered[i]
			if i < total && isCovered {
				coveredCount++
			}
			if !isCovered && i < total {
				if gapStart == -1 {
					gapStart = i
				}
				continue
			}
			if gapStart != -1 {
				lineStart := seg.LineStart + gapStart
				lineEnd := seg.LineStart + i - 1
				gaps = append(gaps, CoverageGap{
					LineStart: lineStart,
					LineEnd:   lineEnd,
					Preview:   previewLines(mdLines, lineStart, lineEnd),
				})
				gapStart = -1
			}
		}

		reports = append(reports, SegmentCoverage{
			OutlineTitle:        seg.Title,
			LineStart:           seg.LineStart,
			LineEnd:             seg.LineEnd,
			TotalLines:          total,
			CoveredLines:        coveredCount,
			Gaps:                gaps,
			HasExtractionFailed: hasFailed,
		})
	}

	segRanges := make([]lineRange, len(segments))
	for i, seg := range segments {
		segRanges[i] = lineRange{start: seg.LineStart, end: seg.LineEnd}
	}
	for _, gap := range findUncoveredRanges(segRanges, len(mdLines)) {
		reports = append(reports, SegmentCoverage{
			OutlineTitle: "（不属于任何 segment）",
			LineStart:    gap.start,
			LineEnd:      gap.end,
			TotalLines:   gap.end - gap.start + 1,
			CoveredLines: 0,
			Gaps: []CoverageGap{{
				LineStart: gap.start,
				LineEnd:   gap.end,
				Preview:   previewLines(mdLines, gap.start, gap.end),
			}},
		})
	}

	return reports
}

// lineRange is an inclusive, 1-indexed [start, end] line range.
type lineRange struct {
	start, end int
}

// findUncoveredRanges returns the gaps in [1, totalLines] not covered by any
// of the given ranges, as contiguous runs. Shared by ComputeCoverage (segment
// list vs. the whole document) and uncoveredSegments in segment.go (leaf
// outline nodes vs. the whole document) — same gap-scanning logic, two
// different "what should already cover everything" assumptions to verify.
func findUncoveredRanges(ranges []lineRange, totalLines int) []lineRange {
	if totalLines <= 0 {
		return nil
	}
	covered := make([]bool, totalLines+1) // 1-indexed; index 0 unused
	for _, r := range ranges {
		start, end := r.start, r.end
		if start < 1 {
			start = 1
		}
		if end > totalLines {
			end = totalLines
		}
		for line := start; line <= end; line++ {
			covered[line] = true
		}
	}

	var gaps []lineRange
	gapStart := -1
	for line := 1; line <= totalLines; line++ {
		if !covered[line] {
			if gapStart == -1 {
				gapStart = line
			}
			continue
		}
		if gapStart != -1 {
			gaps = append(gaps, lineRange{start: gapStart, end: line - 1})
			gapStart = -1
		}
	}
	if gapStart != -1 {
		gaps = append(gaps, lineRange{start: gapStart, end: totalLines})
	}
	return gaps
}

func previewLines(mdLines []string, lineStart, lineEnd int) string {
	from := lineStart
	if from < 1 {
		from = 1
	}
	to := lineEnd
	if to > len(mdLines) {
		to = len(mdLines)
	}
	if from > to {
		return ""
	}
	text := ""
	for i := from; i <= to; i++ {
		if i > from {
			text += " "
		}
		text += mdLines[i-1]
	}
	r := []rune(text)
	if len(r) > gapPreviewMaxRunes {
		return string(r[:gapPreviewMaxRunes])
	}
	return text
}
