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
	// OutlinePath is the root→leaf outline title chain joined with " / "
	// (same format as Store.GetOutlinePath). Fed to unit_point_extract as
	// ownership context so center/KP labeling stays anchored when a unit's
	// own lines omit the section heading. Empty OutlineID → "（无目录）".
	OutlinePath string
	LineStart   int
	LineEnd     int
}

const emptyOutlinePath = "（无目录）"

// outlinePathForID walks parent_id upward and joins titles root→leaf with
// " / ". Missing id or broken chain returns emptyOutlinePath.
func outlinePathForID(outlines []source.Outline, outlineID string) string {
	if outlineID == "" {
		return emptyOutlinePath
	}
	byID := make(map[string]source.Outline, len(outlines))
	for _, o := range outlines {
		byID[o.OutlineID] = o
	}
	var titles []string
	id := outlineID
	for depth := 0; id != "" && depth < 32; depth++ {
		o, ok := byID[id]
		if !ok {
			break
		}
		titles = append([]string{o.Title}, titles...)
		if !o.ParentID.Valid {
			break
		}
		id = o.ParentID.String
	}
	if len(titles) == 0 {
		return emptyOutlinePath
	}
	return strings.Join(titles, " / ")
}

func outlinePathForNullID(outlines []source.Outline, outlineID sql.NullString) string {
	if !outlineID.Valid {
		return emptyOutlinePath
	}
	return outlinePathForID(outlines, outlineID.String)
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
		oid := sql.NullString{String: leaf.OutlineID, Valid: true}
		candidates = append(candidates, Segment{
			OutlineID:   oid,
			Title:       leaf.Title,
			OutlinePath: outlinePathForID(outlines, leaf.OutlineID),
			LineStart:   leaf.LineStart,
			LineEnd:     leaf.LineEnd,
		})
	}
	// 补上没有任何叶节点认领的行区间（如标题误判把后续内容并入错误的标题辖区、
	// 文档开头在第一个标题之前的正文）——否则这段内容在 Unit 提取阶段永远不会
	// 被看到，也不会出现在 SourceCoverageReport 的缺口里（2026-07-16 QA 排查，
	// 见 docs/impl/mvp/unit.md 覆盖率相关讨论）。
	candidates = append(candidates, uncoveredSegments(leaves, outlines, markdownLines)...)

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
//
// The gap segment's OutlineID is resolved to the deepest outline node that
// fully covers the gap (matchOutlineByLineRange), not left null — a null
// outline_id makes the resulting KU invisible to outline-structure recall.
// This is a fallback, not a substitute for closing gaps at generation time
// (see repairSections in internal/source/semantic.go): it only gets a KU
// attached to whichever ancestor spans the gap, not the precise leaf a
// tighter-generated outline would have given it (2026-08-11 排查,
// docs/impl/v1/retrieval.md).
func uncoveredSegments(leaves []source.Outline, outlines []source.Outline, markdownLines []string) []Segment {
	leafRanges := make([]lineRange, len(leaves))
	for i, leaf := range leaves {
		leafRanges[i] = lineRange{start: leaf.LineStart, end: leaf.LineEnd}
	}

	var segs []Segment
	for _, gap := range findUncoveredRanges(leafRanges, len(markdownLines)) {
		hasContent := false
		for line := gap.start; line <= gap.end; line++ {
			if strings.TrimSpace(markdownLines[line-1]) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			continue
		}
		oid := matchOutlineByLineRange(outlines, gap.start, gap.end)
		segs = append(segs, Segment{
			OutlineID:   oid,
			Title:       "未识别标题内容",
			OutlinePath: outlinePathForNullID(outlines, oid),
			LineStart:   gap.start,
			LineEnd:     gap.end,
		})
	}
	return segs
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

// leafBoundaryKey identifies the leaf outline a segment belongs to, for the
// merge guard in mergeSmallSegments. Segments produced from an actual leaf
// (BuildSegments' candidates) already carry that leaf's OutlineID — merging
// must never cross into a sibling leaf, even under the same top-level
// ancestor, or the merged KU's outline_id ends up pointing at the shared
// ancestor instead of either leaf (see docs/impl/v1/retrieval.md 目录结构
// 检索 gap, 2026-08-11 排查). Gap segments (uncoveredSegments) have no leaf
// of their own, so they fall back to the top-level-ancestor grouping that
// was the sole guard before this fix.
func leafBoundaryKey(seg Segment, outlines []source.Outline) string {
	if seg.OutlineID.Valid {
		return seg.OutlineID.String
	}
	return topAncestorAtLine(outlines, seg.LineStart)
}

func mergeSmallSegments(segments []Segment, outlines []source.Outline, lines []string, minChars int) []Segment {
	if len(segments) <= 1 {
		return segments
	}

	// Forward pass: merge small segments into next neighbor, but never across
	// a different leaf outline node (leafBoundaryKey) — a small trailing piece
	// of one leaf must not get glued to a sibling leaf just because both are
	// short, even when they share a common ancestor. curLeaf is captured once
	// from the original (pre-merge) segment, before any absorption happens, so
	// the whole merge run stays anchored to the leaf it actually started in.
	var forward []Segment
	for i := 0; i < len(segments); i++ {
		cur := segments[i]
		curLeaf := leafBoundaryKey(cur, outlines)
		merged := false
		for i+1 < len(segments) && segmentCharCount(cur, lines) < minChars {
			next := segments[i+1]
			if leafBoundaryKey(next, outlines) != curLeaf {
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
			cur.OutlinePath = outlinePathForNullID(outlines, cur.OutlineID)
		}
		forward = append(forward, cur)
	}

	// Backward pass: if the last segment is still small, merge into previous —
	// same leaf-boundary guard applies.
	if len(forward) >= 2 {
		last := &forward[len(forward)-1]
		prev := &forward[len(forward)-2]
		if segmentCharCount(*last, lines) < minChars &&
			leafBoundaryKey(*last, outlines) == leafBoundaryKey(*prev, outlines) {
			prev.LineEnd = last.LineEnd
			prev.OutlineID = matchOutlineByLineRange(outlines, prev.LineStart, prev.LineEnd)
			prev.OutlinePath = outlinePathForNullID(outlines, prev.OutlineID)
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
