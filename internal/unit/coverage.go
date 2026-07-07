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
func ComputeCoverage(segments []Segment, units []KnowledgeUnit, mdLines []string) []SegmentCoverage {
	reports := make([]SegmentCoverage, 0, len(segments))

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

	return reports
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
