package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/textmatch"
)

const (
	promptVersionBoundaryExtract = "v3"
	promptVersionPointExtract    = "v3"
	// promptVersionSplitExtract tags candidates produced by the two-step
	// boundary+point split pipeline — derived from the two prompts' own
	// versions so it can never silently drift out of sync with them the way
	// a hand-written combined tag once did.
	promptVersionSplitExtract = "split:" + promptVersionBoundaryExtract + "+" + promptVersionPointExtract
)

var numberedLineRE = regexp.MustCompile(`^\[(\d+)\]\s?(.*)$`)

type boundaryExtractOutput struct {
	Units []boundaryUnit `json:"units"`
}

type boundaryUnit struct {
	Center  string             `json:"center"`
	Content []boundaryLineItem `json:"content"`
}

type boundaryLineItem string

func (b *boundaryLineItem) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*b = boundaryLineItem(s)
		return nil
	}
	*b = boundaryLineItem(string(data))
	return nil
}

type pointExtractOutput struct {
	Center string      `json:"center"`
	Points []pointOnly `json:"points"`
}

type pointOnly struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

// extractSegmentOutputSplit runs one segment's extraction: a single
// unit_boundary_extract.md call decides the units' line boundaries, then one
// unit_point_extract.md call per unit extracts its knowledge points. This is
// the only extraction path — the pre-V1 single-call unit_extract.md flow was
// removed once the split flow became the default.
func (s *Service) extractSegmentOutputSplit(ctx context.Context, sourceID string, seg Segment, mdLines []string) (extractOutput, bool, error) {
	textContent := sliceLinesWithLineNumbers(mdLines, seg.LineStart, seg.LineEnd)
	vars := map[string]string{
		"outline_title":      seg.Title,
		"segment_line_start": strconv.Itoa(seg.LineStart),
		"segment_line_end":   strconv.Itoa(seg.LineEnd),
		"text_content":       textContent,
	}

	data, err := s.llmClient.CompleteJSON(ctx, "unit_boundary_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: split boundary extraction failed", "source_id", sourceID, "line_start", seg.LineStart, "line_end", seg.LineEnd, "error", err)
		return extractOutput{}, false, fmt.Errorf("split boundary extraction: %w", err)
	}

	var boundary boundaryExtractOutput
	if err := json.Unmarshal(data, &boundary); err != nil {
		slog.Warn("unit: split boundary JSON parse failed", "source_id", sourceID, "line_start", seg.LineStart, "line_end", seg.LineEnd, "error", err)
		return extractOutput{}, false, fmt.Errorf("split boundary parse: %w", err)
	}

	output := extractOutput{}
	for i, bu := range boundary.Units {
		u, unitContent, _, ok := buildLLMUnitFromBoundary(seg, mdLines, bu, i+1)
		if !ok {
			slog.Info("unit: split boundary unit rejected", "source_id", sourceID, "line_start", seg.LineStart, "line_end", seg.LineEnd, "center", bu.Center)
			continue
		}

		center, points, ok, err := s.extractPointsForSplitUnit(ctx, u, unitContent)
		if err != nil {
			return extractOutput{}, false, fmt.Errorf("split point extraction for unit %s lines %d-%d: %w", u.UnitID, u.LineStart, u.LineEnd, err)
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(center) != "" {
			u.Center = strings.TrimSpace(center)
		}
		output.Units = append(output.Units, u)
		output.Points = append(output.Points, points...)
	}

	return output, len(output.Units) > 0, nil
}

// extractPointsForSplitUnit deliberately does NOT pass u.Center to the
// prompt: that center is a first-line-derived fallback
// (buildLLMUnitFromBoundary), and feeding it in anchored the model on the
// unit's opening topic — a multi-topic unit then got points for its head
// only (差旅费报销制度 incident: a 住宿费-flavored fallback center left the
// entire 第六条交通费用 half of the unit with zero points). The point model
// derives the center from the full content itself.
func (s *Service) extractPointsForSplitUnit(ctx context.Context, u llmUnit, unitContent string) (string, []llmPoint, bool, error) {
	vars := map[string]string{
		"unit_line_start": strconv.Itoa(u.LineStart),
		"unit_line_end":   strconv.Itoa(u.LineEnd),
		"unit_content":    unitContent,
	}
	data, err := s.llmClient.CompleteJSON(ctx, "unit_point_extract.md", vars, "extraction")
	if err != nil {
		slog.Warn("unit: split point extraction failed", "center", u.Center, "line_start", u.LineStart, "line_end", u.LineEnd, "error", err)
		return "", nil, false, err
	}
	var out pointExtractOutput
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("unit: split point JSON parse failed", "center", u.Center, "line_start", u.LineStart, "line_end", u.LineEnd, "error", err)
		return "", nil, false, err
	}
	points := make([]llmPoint, 0, len(out.Points))
	for i, p := range out.Points {
		if strings.TrimSpace(p.Content) == "" || strings.TrimSpace(p.Type) == "" {
			continue
		}
		points = append(points, llmPoint{
			PointID:         fmt.Sprintf("p%d", i+1),
			UnitID:          u.UnitID,
			Content:         p.Content,
			Type:            p.Type,
			LineStart:       u.LineStart,
			FirstLineAnchor: u.FirstLineAnchor,
			LineEnd:         u.LineEnd,
			LastLineAnchor:  u.LastLineAnchor,
		})
	}
	return out.Center, points, len(points) > 0, nil
}

func buildLLMUnitFromBoundary(seg Segment, mdLines []string, bu boundaryUnit, index int) (llmUnit, string, []int, bool) {
	center := strings.TrimSpace(bu.Center)
	if len(bu.Content) == 0 {
		return llmUnit{}, "", nil, false
	}
	seen := make(map[int]bool)
	lines := make([]int, 0, len(bu.Content))
	for _, raw := range bu.Content {
		lineNo, text, ok := parseNumberedLine(string(raw))
		if !ok || lineNo < seg.LineStart || lineNo > seg.LineEnd || lineNo < 1 || lineNo > len(mdLines) || seen[lineNo] {
			continue
		}
		// The model selects line numbers; the program owns the canonical text.
		// A mismatch is repairable because mdLines[lineNo-1] is the source of
		// truth. Keeping the line prevents one copied SQL/config typo from
		// dropping a whole large technical unit.
		if _, _, _, ok := textmatch.MatchFragment(mdLines[lineNo-1], text); !ok {
			// Intentionally tolerated.
		}
		seen[lineNo] = true
		lines = append(lines, lineNo)
	}
	if len(lines) == 0 {
		return llmUnit{}, "", nil, false
	}
	sort.Ints(lines)

	lineStart := lines[0]
	lineEnd := lines[len(lines)-1]
	lines = fillSmallBoundaryGaps(lines, lineStart, lineEnd)
	if center == "" {
		center = fallbackCenterFromLines(mdLines, lineStart, lineEnd)
	}
	unitContent := sliceLinesWithLineNumbers(mdLines, lineStart, lineEnd)
	return llmUnit{
		UnitID:          fmt.Sprintf("u%d", index),
		Center:          center,
		LineStart:       lineStart,
		FirstLineAnchor: mdLines[lineStart-1],
		LineEnd:         lineEnd,
		LastLineAnchor:  mdLines[lineEnd-1],
	}, unitContent, lines, true
}

func fillSmallBoundaryGaps(lines []int, lineStart, lineEnd int) []int {
	if len(lines) == 0 {
		return lines
	}
	seen := make(map[int]bool, len(lines))
	for _, line := range lines {
		seen[line] = true
	}
	for line := lineStart; line <= lineEnd; line++ {
		if !seen[line] {
			lines = append(lines, line)
		}
	}
	sort.Ints(lines)
	return lines
}

func fallbackCenterFromLines(mdLines []string, lineStart, lineEnd int) string {
	for line := lineStart; line <= lineEnd; line++ {
		if line < 1 || line > len(mdLines) {
			continue
		}
		text := strings.TrimSpace(mdLines[line-1])
		text = strings.TrimLeft(text, "#*-0123456789.、)） \t")
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 30 {
			runes = runes[:30]
		}
		return string(runes)
	}
	return "未命名知识单元"
}

func parseNumberedLine(raw string) (int, string, bool) {
	m := numberedLineRE.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 3 {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(m[2]), true
}
