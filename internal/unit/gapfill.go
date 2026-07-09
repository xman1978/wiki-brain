package unit

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// resolvedUnit is the subset of a just-inserted KnowledgeUnit that gap
// filling needs: which lines it ended up covering, so a later gap can find
// its nearest neighbor and so the segment's overall coverage can be
// recomputed without a DB round-trip.
type resolvedUnit struct {
	unitID    string
	lineStart int
	lineEnd   int
}

// gapRanges returns the contiguous line ranges within [segStart, segEnd] not
// covered by any of the given (lineStart, lineEnd) unit ranges — the same
// algorithm ComputeCoverage uses, but operating on in-memory ranges from the
// extraction pass just run instead of re-reading units back from SQLite.
func gapRanges(segStart, segEnd int, covered [][2]int) [][2]int {
	total := segEnd - segStart + 1
	if total <= 0 {
		return nil
	}
	isCovered := make([]bool, total)
	for _, r := range covered {
		from, to := r[0], r[1]
		if from < segStart {
			from = segStart
		}
		if to > segEnd {
			to = segEnd
		}
		for line := from; line <= to; line++ {
			isCovered[line-segStart] = true
		}
	}

	var gaps [][2]int
	start := -1
	for i := 0; i <= total; i++ {
		ok := i < total && isCovered[i]
		if i < total && !ok {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			gaps = append(gaps, [2]int{segStart + start, segStart + i - 1})
			start = -1
		}
	}
	return gaps
}

// isTrivialGap reports whether a gap's raw text is pure markdown scaffolding
// (headings, blank lines, horizontal rules, table separator rows) with no
// content of its own — safe to merge into a neighboring unit without
// spending an LLM call to find out. Anything else is sent through
// extractGap so the model itself judges whether it deserves its own unit.
func isTrivialGap(mdLines []string, lineStart, lineEnd int) bool {
	for i := lineStart; i <= lineEnd; i++ {
		if i < 1 || i > len(mdLines) {
			continue
		}
		line := strings.TrimSpace(mdLines[i-1])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if isRuleOrSeparatorRow(line) {
			continue
		}
		return false
	}
	return true
}

// isRuleOrSeparatorRow matches markdown table separator rows ("| --- | --- |")
// and horizontal rules ("---", "==="): punctuation-only lines with nothing
// extractable in them.
func isRuleOrSeparatorRow(line string) bool {
	stripped := strings.Map(func(r rune) rune {
		switch r {
		case '|', '-', ':', ' ', '=':
			return -1
		}
		return r
	}, line)
	return stripped == ""
}

// gapContextText renders the gap's own lines inside the rest of the parent
// segment, clearly marked off from the surrounding context on both sides so
// the model can use the full segment (an enclosing IF/loop header, a
// preceding definition, etc.) to understand what the gap's lines mean
// without mistaking the context for something it should also extract units
// from. When the gap is the entire segment (no context on either side), this
// degrades to plain numbered lines, same as sliceLinesWithLineNumbers.
func gapContextText(mdLines []string, seg Segment, gapStart, gapEnd int) string {
	var sb strings.Builder
	if gapStart > seg.LineStart {
		sb.WriteString("[以下第 " + strconv.Itoa(seg.LineStart) + "-" + strconv.Itoa(gapStart-1) + " 行是上下文，仅供理解，不要在这些行上生成 unit]\n")
		sb.WriteString(sliceLinesWithLineNumbers(mdLines, seg.LineStart, gapStart-1))
	}
	sb.WriteString("[以下第 " + strconv.Itoa(gapStart) + "-" + strconv.Itoa(gapEnd) + " 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n")
	sb.WriteString(sliceLinesWithLineNumbers(mdLines, gapStart, gapEnd))
	if gapEnd < seg.LineEnd {
		sb.WriteString("[以下第 " + strconv.Itoa(gapEnd+1) + "-" + strconv.Itoa(seg.LineEnd) + " 行是上下文，仅供理解，不要在这些行上生成 unit]\n")
		sb.WriteString(sliceLinesWithLineNumbers(mdLines, gapEnd+1, seg.LineEnd))
	}
	return sb.String()
}

// fillGaps runs once per segment after all its units have been resolved and
// inserted (see processSegment). Lines the model's units didn't cover are
// either given their own unit — if extractGap finds the content substantive
// enough on its own — or merged into the nearest neighboring unit's bounds.
// A segment with zero resolved units is a whole-segment extraction failure
// already handled by the caller's retry path; there is no neighbor to merge
// into, so it's left alone here.
func (s *Service) fillGaps(ctx context.Context, sourceID string, seg Segment, mdLines []string, resolved []resolvedUnit) {
	if len(resolved) == 0 {
		return
	}

	ranges := make([][2]int, len(resolved))
	for i, r := range resolved {
		ranges[i] = [2]int{r.lineStart, r.lineEnd}
	}

	for _, gap := range gapRanges(seg.LineStart, seg.LineEnd, ranges) {
		gapStart, gapEnd := gap[0], gap[1]

		if !isTrivialGap(mdLines, gapStart, gapEnd) {
			if extra := s.extractGap(ctx, sourceID, seg, mdLines, gapStart, gapEnd); len(extra) > 0 {
				resolved = append(resolved, extra...)
				continue
			}
		}
		resolved = s.mergeGapIntoNeighbor(mdLines, resolved, gapStart, gapEnd)
	}
}

// extractGap runs a scoped re-extraction pass over [gapStart, gapEnd],
// reusing unit_extract.md exactly as processSegment does for a full segment.
// An empty units array is a valid, meaningful response — it means the model
// judges this content not worth its own unit — in which case the caller
// falls back to merging the gap into a neighbor.
//
// The gap's own lines are framed inside the rest of the parent segment as
// read-only context rather than sent in isolation: a gap that's really a
// truncated slice out of the middle of continuous code or prose (e.g. a few
// assignments from inside an IF branch whose header sits outside the gap,
// already absorbed into a neighboring unit) is unintelligible on its own —
// the model either can't judge it and hallucinates a plausible-sounding but
// decontextualized unit, or correctly gives up in a way indistinguishable
// from "this really is standalone noise". Showing the whole segment lets it
// tell those apart. LocateUnitBounds is still constrained to gapSeg
// (gapStart..gapEnd, see below), so any unit the model anchors inside the
// context region — despite being told not to — simply fails to locate and
// is dropped, the same as any other hallucinated anchor.
func (s *Service) extractGap(ctx context.Context, sourceID string, seg Segment, mdLines []string, gapStart, gapEnd int) []resolvedUnit {
	textContent := gapContextText(mdLines, seg, gapStart, gapEnd)
	schemaJSON := `{"units":[{"unit_id":"1","center":"知识单元主题","line_start":5,"first_line_anchor":"第5行本身的原文","line_end":8,"last_line_anchor":"第8行本身的原文"}],"points":[{"point_id":"1","unit_id":"1","content":"可激活摘要内容","type":"definition|rule|method|case|question"}]}`

	vars := map[string]string{
		"outline_title":      seg.Title,
		"segment_line_start": strconv.Itoa(gapStart),
		"segment_line_end":   strconv.Itoa(gapEnd),
		"text_content":       textContent,
		"json_schema":        schemaJSON,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: gap extraction call failed", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "error", err)
		return nil
	}

	var output extractOutput
	if err := json.Unmarshal(data, &output); err != nil {
		slog.Warn("unit: gap extraction JSON parse failed", "source_id", sourceID, "line_start", gapStart, "line_end", gapEnd, "error", err)
		return nil
	}

	gapSeg := Segment{OutlineID: seg.OutlineID, Title: seg.Title, LineStart: gapStart, LineEnd: gapEnd}

	var out []resolvedUnit
	cursor := gapSeg.LineStart
	for _, u := range output.Units {
		lineStart, lineEnd, nextCursor, locateOK := LocateUnitBounds(mdLines, gapSeg, u.LineStart, u.FirstLineAnchor, u.LineEnd, u.LastLineAnchor, cursor)
		if locateOK {
			cursor = nextCursor
		}
		if !locateOK || !s.validateUnit(u, output.Points) {
			continue
		}

		unitID := uuid.New().String()
		ku := &KnowledgeUnit{
			UnitID:        unitID,
			SourceID:      sourceID,
			OutlineID:     seg.OutlineID,
			Center:        u.Center,
			LineStart:     lineStart,
			LineEnd:       lineEnd,
			Status:        "completed",
			PromptVersion: "v5",
		}
		if err := s.store.InsertUnit(ku); err != nil {
			slog.Error("unit: gap fill insert unit failed", "error", err)
			continue
		}

		for _, p := range output.Points {
			if p.UnitID != u.UnitID {
				continue
			}
			kp := &KnowledgePoint{
				PointID:   uuid.New().String(),
				UnitID:    unitID,
				SourceID:  sourceID,
				Content:   p.Content,
				PointType: p.Type,
			}
			if err := s.store.InsertPoint(kp); err != nil {
				slog.Error("unit: gap fill insert point failed", "error", err)
				continue
			}
			s.indexPoint(kp)
		}

		s.indexUnit(ku, mdLines)
		out = append(out, resolvedUnit{unitID: unitID, lineStart: lineStart, lineEnd: lineEnd})
	}

	return out
}

// mergeGapIntoNeighbor absorbs [gapStart, gapEnd] into whichever resolved
// unit sits closest to it (by line distance), widening that unit's
// line_start/line_end and persisting the new bounds. Returns resolved with
// the merged-into entry updated in place, so later gaps in the same segment
// see the widened neighbor when computing their own nearest match.
func (s *Service) mergeGapIntoNeighbor(mdLines []string, resolved []resolvedUnit, gapStart, gapEnd int) []resolvedUnit {
	best := -1
	bestDist := 0
	for i, r := range resolved {
		var dist int
		switch {
		case gapStart > r.lineEnd:
			dist = gapStart - r.lineEnd
		case gapEnd < r.lineStart:
			dist = r.lineStart - gapEnd
		default:
			continue // already overlaps — shouldn't happen, skip
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best == -1 {
		return resolved
	}

	target := resolved[best]
	newStart, newEnd := target.lineStart, target.lineEnd
	if gapStart < newStart {
		newStart = gapStart
	}
	if gapEnd > newEnd {
		newEnd = gapEnd
	}
	if newStart == target.lineStart && newEnd == target.lineEnd {
		return resolved
	}

	if err := s.store.UpdateUnitBounds(target.unitID, newStart, newEnd); err != nil {
		slog.Error("unit: merge gap into neighbor failed", "unit_id", target.unitID, "error", err)
		return resolved
	}
	resolved[best].lineStart = newStart
	resolved[best].lineEnd = newEnd

	if ku, err := s.store.GetUnitByID(target.unitID); err == nil {
		s.indexUnit(ku, mdLines)
	}
	return resolved
}
