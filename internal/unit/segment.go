package unit

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jxman78/wiki-brain/internal/source"
)

type Segment struct {
	OutlineID sql.NullString
	Title     string
	LineStart int
	LineEnd   int
}

// BuildSegments 将 outline 叶节点转为提取分段。
// Source 模块保证所有叶节点内容 ≤ segment_max_chars，此处只做小段合并，不做硬切。
func BuildSegments(outlines []source.Outline, markdownLines []string, segmentMaxChars, minSegmentChars int) []Segment {
	if minSegmentChars <= 0 {
		minSegmentChars = 400
	}

	leaves := findLeaves(outlines)

	candidates := make([]Segment, 0, len(leaves))
	for _, leaf := range leaves {
		candidates = append(candidates, Segment{
			OutlineID: sql.NullString{String: leaf.OutlineID, Valid: true},
			Title:     leaf.Title,
			LineStart: leaf.LineStart,
			LineEnd:   leaf.LineEnd,
		})
	}
	// 补上没有任何叶节点认领的行区间（如标题误判把后续内容并入错误的标题辖区、
	// 文档开头在第一个标题之前的正文）——否则这段内容在 Unit 提取阶段永远不会
	// 被看到，也不会出现在 SourceCoverageReport 的缺口里（2026-07-16 QA 排查，
	// 见 docs/impl/mvp/unit.md 覆盖率相关讨论）。
	candidates = append(candidates, uncoveredSegments(leaves, markdownLines)...)

	rawSegments := make([]Segment, 0, len(candidates))
	for _, seg := range candidates {
		if isMetadataSegment(markdownLines, seg.LineStart, seg.LineEnd, seg.Title) {
			continue
		}
		rawSegments = append(rawSegments, seg)
	}
	if len(rawSegments) == 0 {
		return nil
	}

	sort.Slice(rawSegments, func(i, j int) bool {
		return rawSegments[i].LineStart < rawSegments[j].LineStart
	})

	return mergeSmallSegments(rawSegments, outlines, markdownLines, minSegmentChars)
}

// uncoveredSegments returns one synthetic Segment per contiguous line range
// in [1, len(markdownLines)] that no leaf covers and that has at least one
// non-blank line (an all-blank gap — e.g. the lone empty line of a truly
// empty document — has nothing worth an extraction call). A leaf's own line
// range can miss part of the document even when every leaf is individually
// well-formed — e.g. a misdetected heading whose declared range absorbs
// unrelated content up to the next real heading, leaving that real heading's
// parent's own range uncovered by any leaf; or plain text before the first
// detected heading, which never becomes part of any outline node at all.
// Without this, such ranges silently never reach Unit extraction.
func uncoveredSegments(leaves []source.Outline, markdownLines []string) []Segment {
	totalLines := len(markdownLines)
	if totalLines <= 0 {
		return nil
	}
	covered := make([]bool, totalLines+1) // 1-indexed; index 0 unused
	for _, leaf := range leaves {
		start, end := leaf.LineStart, leaf.LineEnd
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

	addGap := func(gaps []Segment, start, end int) []Segment {
		hasContent := false
		for line := start; line <= end; line++ {
			if strings.TrimSpace(markdownLines[line-1]) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			return gaps
		}
		return append(gaps, Segment{Title: "未识别标题内容", LineStart: start, LineEnd: end})
	}

	var gaps []Segment
	gapStart := -1
	for line := 1; line <= totalLines; line++ {
		if !covered[line] {
			if gapStart == -1 {
				gapStart = line
			}
			continue
		}
		if gapStart != -1 {
			gaps = addGap(gaps, gapStart, line-1)
			gapStart = -1
		}
	}
	if gapStart != -1 {
		gaps = addGap(gaps, gapStart, totalLines)
	}
	return gaps
}

func findLeaves(outlines []source.Outline) []source.Outline {
	parentIDs := make(map[string]bool)
	for _, o := range outlines {
		if o.ParentID.Valid {
			parentIDs[o.ParentID.String] = true
		}
	}

	var leaves []source.Outline
	for _, o := range outlines {
		if !parentIDs[o.OutlineID] {
			leaves = append(leaves, o)
		}
	}
	return leaves
}

func sliceLinesWithLineNumbers(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	if lineStart > lineEnd {
		return ""
	}
	var sb strings.Builder
	for i := lineStart; i <= lineEnd; i++ {
		fmt.Fprintf(&sb, "[%d] %s\n", i, lines[i-1])
	}
	return sb.String()
}

func sliceLines(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	if lineStart > lineEnd {
		return ""
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}

// isMetadataSegment detects TOC sections (mostly markdown links) and
// document metadata blocks (version tables, file headers) that have
// no knowledge extraction value.
func isMetadataSegment(lines []string, lineStart, lineEnd int, title string) bool {
	titleLower := strings.ToLower(title)
	if titleLower == "目录" || titleLower == "table of contents" || titleLower == "toc" {
		return true
	}
	if lineStart < 1 || lineEnd > len(lines) || lineStart > lineEnd {
		return false
	}

	total := 0
	tocLines := 0
	emptyLines := 0
	for i := lineStart - 1; i < lineEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		total++
		if trimmed == "" {
			emptyLines++
			continue
		}
		if strings.HasPrefix(trimmed, "- [") && strings.Contains(trimmed, "](#") {
			tocLines++
		}
	}

	contentLines := total - emptyLines
	if contentLines > 0 && float64(tocLines)/float64(contentLines) > 0.7 {
		return true
	}
	return false
}

func segmentCharCount(seg Segment, lines []string) int {
	return utf8.RuneCountInString(sliceLines(lines, seg.LineStart, seg.LineEnd))
}

func mergeSmallSegments(segments []Segment, outlines []source.Outline, lines []string, minChars int) []Segment {
	if len(segments) <= 1 {
		return segments
	}

	// Forward pass: merge small segments into next neighbor, but never across
	// a different top-level structural ancestor (topAncestorAtLine) — a small
	// trailing piece of one chapter must not get glued to the next, unrelated
	// chapter just because both are short. curTop is captured once from the
	// original (pre-merge) segment, before any absorption happens, so the
	// whole merge run stays anchored to the section it actually started in.
	var forward []Segment
	for i := 0; i < len(segments); i++ {
		cur := segments[i]
		curTop := topAncestorAtLine(outlines, cur.LineStart)
		merged := false
		for i+1 < len(segments) && segmentCharCount(cur, lines) < minChars {
			next := segments[i+1]
			if topAncestorAtLine(outlines, next.LineStart) != curTop {
				break
			}
			cur.LineEnd = next.LineEnd
			if cur.Title == "" {
				cur.Title = next.Title
			}
			merged = true
			i++
		}
		if merged {
			cur.OutlineID = matchOutlineByLineRange(outlines, cur.LineStart, cur.LineEnd)
		}
		forward = append(forward, cur)
	}

	// Backward pass: if the last segment is still small, merge into previous —
	// same top-level-ancestor guard applies.
	if len(forward) >= 2 {
		last := &forward[len(forward)-1]
		prev := &forward[len(forward)-2]
		if segmentCharCount(*last, lines) < minChars &&
			topAncestorAtLine(outlines, last.LineStart) == topAncestorAtLine(outlines, prev.LineStart) {
			prev.LineEnd = last.LineEnd
			prev.OutlineID = matchOutlineByLineRange(outlines, prev.LineStart, prev.LineEnd)
			forward = forward[:len(forward)-1]
		}
	}

	return forward
}

// topAncestorAtLine finds the outline node that most tightly covers line
// (the deepest node whose [LineStart,LineEnd] contains it) and walks its
// ParentID chain up to the node with no parent, returning that root node's
// OutlineID. mergeSmallSegments uses this to stop merging once two segments
// belong to different top-level structural sections — e.g. a small trailing
// semantic child of one chapter merging into an entirely different next
// chapter (docs/impl/mvp/unit.md 步骤 1).
func topAncestorAtLine(outlines []source.Outline, line int) string {
	byID := make(map[string]source.Outline, len(outlines))
	for _, o := range outlines {
		byID[o.OutlineID] = o
	}

	var deepest *source.Outline
	for i := range outlines {
		o := &outlines[i]
		if o.LineStart <= line && o.LineEnd >= line {
			if deepest == nil || o.Level > deepest.Level {
				deepest = o
			}
		}
	}
	if deepest == nil {
		return ""
	}

	id := deepest.OutlineID
	for {
		o, ok := byID[id]
		if !ok || !o.ParentID.Valid {
			return id
		}
		id = o.ParentID.String
	}
}

// matchOutlineByLineRange 在合并段后，按新的行范围重新匹配最深层（Level 最大）
// 且行范围完整覆盖该段的 outline 节点；找不到完整覆盖的节点时返回 null，
// 避免沿用合并前某个子节点的 outline_id 导致行范围与归属节点不一致。
func matchOutlineByLineRange(outlines []source.Outline, lineStart, lineEnd int) sql.NullString {
	var best *source.Outline
	for i := range outlines {
		o := &outlines[i]
		if o.LineStart <= lineStart && o.LineEnd >= lineEnd {
			if best == nil || o.Level > best.Level {
				best = o
			}
		}
	}
	if best == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: best.OutlineID, Valid: true}
}
