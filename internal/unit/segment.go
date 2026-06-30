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
	if len(leaves) == 0 {
		return nil
	}

	rawSegments := make([]Segment, 0, len(leaves))
	for _, leaf := range leaves {
		if isMetadataSegment(markdownLines, leaf.LineStart, leaf.LineEnd, leaf.Title) {
			continue
		}
		rawSegments = append(rawSegments, Segment{
			OutlineID: sql.NullString{String: leaf.OutlineID, Valid: true},
			Title:     leaf.Title,
			LineStart: leaf.LineStart,
			LineEnd:   leaf.LineEnd,
		})
	}

	sort.Slice(rawSegments, func(i, j int) bool {
		return rawSegments[i].LineStart < rawSegments[j].LineStart
	})

	return mergeSmallSegments(rawSegments, outlines, markdownLines, minSegmentChars)
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

	// Forward pass: merge small segments into next neighbor
	var forward []Segment
	for i := 0; i < len(segments); i++ {
		cur := segments[i]
		merged := false
		for i+1 < len(segments) && segmentCharCount(cur, lines) < minChars {
			next := segments[i+1]
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

	// Backward pass: if the last segment is still small, merge into previous
	if len(forward) >= 2 {
		last := &forward[len(forward)-1]
		if segmentCharCount(*last, lines) < minChars {
			prev := &forward[len(forward)-2]
			prev.LineEnd = last.LineEnd
			prev.OutlineID = matchOutlineByLineRange(outlines, prev.LineStart, prev.LineEnd)
			forward = forward[:len(forward)-1]
		}
	}

	return forward
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
