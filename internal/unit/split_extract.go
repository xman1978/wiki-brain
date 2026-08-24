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
	"github.com/jxman78/wiki-brain/internal/rerank"
)

const (
	promptVersionBoundaryExtract   = "v3"
	promptVersionPointExtract      = "v8"
	promptVersionPointCoverageFill = "v1"
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
	Content      string `json:"content"`
	Type         string `json:"type"`
	ContentTheme string `json:"content_theme"`
	Object       string `json:"object"`
	Scope        string `json:"scope"`
}

// extractSegmentOutputSplit runs one segment's extraction: a single
// unit_boundary_extract.md call decides the units' line boundaries, then one
// unit_point_extract.md call per unit extracts its knowledge points. This is
// the only extraction path — the pre-V1 single-call unit_extract.md flow was
// removed once the split flow became the default.
func (s *Service) extractSegmentOutputSplit(ctx context.Context, sourceID, sourceTitle, sourceSummary string, seg Segment, mdLines []string) (extractOutput, bool, error) {
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

		center, points, ok, err := s.extractPointsForSplitUnit(ctx, sourceTitle, sourceSummary, seg.OutlinePath, u, unitContent)
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
//
// outlinePath (root→leaf directory titles) is ownership context, not a
// substitute for content: when the unit's own lines omit the section heading
// (e.g. a subsidy table split from "第七条伙食补贴"), the path keeps center/KP
// labeling from inventing a sibling topic like 住宿补贴.
func (s *Service) extractPointsForSplitUnit(ctx context.Context, sourceTitle, sourceSummary, outlinePath string, u llmUnit, unitContent string) (string, []llmPoint, bool, error) {
	if strings.TrimSpace(outlinePath) == "" {
		outlinePath = emptyOutlinePath
	}
	vars := map[string]string{
		"source_title":    sourceTitle,
		"source_summary":  emptyOr(sourceSummary, "（无摘要）"),
		"outline_path":    outlinePath,
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
		point := llmPoint{
			PointID:         fmt.Sprintf("p%d", i+1),
			UnitID:          u.UnitID,
			Content:         p.Content,
			Type:            p.Type,
			LineStart:       u.LineStart,
			FirstLineAnchor: u.FirstLineAnchor,
			LineEnd:         u.LineEnd,
			LastLineAnchor:  u.LastLineAnchor,
			SourceTheme:     strings.TrimSpace(sourceTitle),
			ContentTheme:    strings.TrimSpace(p.ContentTheme),
			Object:          strings.TrimSpace(p.Object),
			Scope:           strings.TrimSpace(p.Scope),
		}
		if point.ContentTheme != "" && point.Scope != "" {
			point.SemanticsPromptVersion = rerank.ExtractPromptVersion
		}
		points = append(points, point)
	}
	if len(points) > 0 {
		points = s.ensurePointsCoverage(ctx, u, unitContent, points)
	}
	return out.Center, points, len(points) > 0, nil
}

// ensurePointsCoverage guards against the extraction model compressing
// several parallel numeric sub-items (table rows / list items) in a busy
// category into one generic sentence and silently dropping the rest — a
// failure mode observed in production (V1 P4 acceptance testing, 2026-08-16:
// a "结果分" table cell with 7 sibling sub-rules had 2 of them, including
// "考试成绩低于80分-1", compressed out of existence, making numeric-specific
// questions about them permanently unanswerable since knowledge_points is
// the only content the rerank relevance judge ever sees). Mirrors
// internal/evidence/service.go's mining validation, direction reversed: that
// checks mined content is a real substring of the source; this checks every
// numeric row/item in the source is mentioned by at least one point.
func (s *Service) ensurePointsCoverage(ctx context.Context, u llmUnit, unitContent string, points []llmPoint) []llmPoint {
	rows := append(detectNumericRowSignatures(unitContent), detectColumnSignatures(unitContent)...)
	missing := uncoveredRows(rows, points)
	if len(missing) == 0 {
		return points
	}
	slog.Info("unit: point coverage gap detected", "unit_id", u.UnitID, "center", u.Center, "missing_rows", len(missing))

	filled, err := s.fillPointsCoverageGap(ctx, u, unitContent, missing)
	if err != nil {
		slog.Warn("unit: point coverage gap fill call failed", "unit_id", u.UnitID, "error", err)
	} else if len(filled) > 0 {
		points = append(points, filled...)
		slog.Info("unit: point coverage gap filled by supplemental extraction", "unit_id", u.UnitID, "added_points", len(filled))
		missing = uncoveredRows(rows, points)
	}

	for _, r := range missing {
		slog.Warn("unit: point coverage gap, verbatim row fallback added", "unit_id", u.UnitID, "row", r.text)
		points = append(points, llmPoint{
			Content:         cleanRowText(r.text),
			Type:            "rule",
			LineStart:       u.LineStart,
			FirstLineAnchor: u.FirstLineAnchor,
			LineEnd:         u.LineEnd,
			LastLineAnchor:  u.LastLineAnchor,
		})
	}
	// PointID renumbered here so fallback/supplemental points get stable,
	// unique local ids consistent with the p%d scheme extractPointsForSplitUnit
	// assigned to the first-pass points above.
	for i := range points {
		points[i].PointID = fmt.Sprintf("p%d", i+1)
		points[i].UnitID = u.UnitID
	}
	return points
}

func (s *Service) fillPointsCoverageGap(ctx context.Context, u llmUnit, unitContent string, missing []rowSignature) ([]llmPoint, error) {
	texts := make([]string, len(missing))
	for i, r := range missing {
		texts[i] = r.text
	}
	vars := map[string]string{
		"unit_content":   unitContent,
		"uncovered_rows": strings.Join(texts, "\n"),
	}
	data, err := s.llmClient.CompleteJSON(ctx, "unit_point_coverage_fill.md", vars, "extraction")
	if err != nil {
		return nil, err
	}
	var out pointExtractOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	points := make([]llmPoint, 0, len(out.Points))
	for _, p := range out.Points {
		if strings.TrimSpace(p.Content) == "" || strings.TrimSpace(p.Type) == "" {
			continue
		}
		points = append(points, llmPoint{
			Content:         p.Content,
			Type:            p.Type,
			LineStart:       u.LineStart,
			FirstLineAnchor: u.FirstLineAnchor,
			LineEnd:         u.LineEnd,
			LastLineAnchor:  u.LastLineAnchor,
		})
	}
	return points, nil
}

// emptyOr returns fallback when s is empty (after trimming) — used for
// optional prompt vars like source_summary that may not exist yet (source
// summary generation is best-effort and can fail, see generateSummary).
func emptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
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
