package unit

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/source"
)

func makeOutline(id, parentID string, level int, title string, lineStart, lineEnd int) source.Outline {
	o := source.Outline{
		OutlineID: id,
		Level:     level,
		Title:     title,
		LineStart: lineStart,
		LineEnd:   lineEnd,
		NodeType:  "structural",
	}
	if parentID != "" {
		o.ParentID = sql.NullString{String: parentID, Valid: true}
	}
	return o
}

func TestBuildSegments_BasicLeafNodes(t *testing.T) {
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 60),
		makeOutline("ch1", "root", 2, "Chapter 1", 1, 30),
		makeOutline("ch2", "root", 2, "Chapter 2", 31, 60),
	}

	lines := make([]string, 60)
	for i := range lines {
		lines[i] = strings.Repeat("这是测试内容", 5) // 30 runes per line
	}

	// Each chapter: 30 lines * ~31 chars = ~930 chars, well above minSegmentChars=400
	segments := BuildSegments(outlines, lines, 4000, 400)

	if len(segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(segments))
	}
	if segments[0].Title != "Chapter 1" {
		t.Errorf("segment[0].Title = %q, want Chapter 1", segments[0].Title)
	}
	if segments[0].LineStart != 1 || segments[0].LineEnd != 30 {
		t.Errorf("segment[0] range = %d-%d, want 1-30", segments[0].LineStart, segments[0].LineEnd)
	}
	if segments[1].LineStart != 31 || segments[1].LineEnd != 60 {
		t.Errorf("segment[1] range = %d-%d, want 31-60", segments[1].LineStart, segments[1].LineEnd)
	}
}

func TestBuildSegments_LargeLeafPassthrough(t *testing.T) {
	// Source guarantees leaf ≤ segment_max_chars, so large leaves pass through as-is
	outlines := []source.Outline{
		makeOutline("leaf", "", 1, "Big Section", 1, 100),
	}

	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("字", 50)
	}

	segments := BuildSegments(outlines, lines, 200, 50)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment (no hard split), got %d", len(segments))
	}
	if segments[0].LineStart != 1 || segments[0].LineEnd != 100 {
		t.Errorf("segment range = %d-%d, want 1-100", segments[0].LineStart, segments[0].LineEnd)
	}
}

func TestBuildSegments_MergeSmallSegments(t *testing.T) {
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 6),
		makeOutline("a", "root", 2, "A", 1, 2),
		makeOutline("b", "root", 2, "B", 3, 4),
		makeOutline("c", "root", 2, "C", 5, 6),
	}

	lines := []string{"a", "b", "c", "d", "e", "f"}

	segments := BuildSegments(outlines, lines, 4000, 400)

	if len(segments) != 1 {
		t.Fatalf("expected 1 merged segment, got %d", len(segments))
	}
	if segments[0].LineStart != 1 || segments[0].LineEnd != 6 {
		t.Errorf("merged range = %d-%d, want 1-6", segments[0].LineStart, segments[0].LineEnd)
	}
}

func TestFindLeaves(t *testing.T) {
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 100),
		makeOutline("ch1", "root", 2, "Ch1", 1, 50),
		makeOutline("sec1", "ch1", 3, "Sec1", 1, 25),
		makeOutline("sec2", "ch1", 3, "Sec2", 26, 50),
		makeOutline("ch2", "root", 2, "Ch2", 51, 100),
	}

	leaves := findLeaves(outlines)
	if len(leaves) != 3 {
		t.Fatalf("got %d leaves, want 3 (sec1, sec2, ch2)", len(leaves))
	}
}

func TestBuildSegments_MergeSmallIntoLargeNeighbor(t *testing.T) {
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 50),
		makeOutline("a", "root", 2, "A", 1, 20),   // 20 lines * 31 chars = 620 chars
		makeOutline("b", "root", 2, "B", 21, 22),   // 2 lines = 62 chars (< 400)
		makeOutline("c", "root", 2, "C", 23, 50),   // 28 lines = 868 chars
	}

	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("字", 30) // 30 runes + newline
	}

	segments := BuildSegments(outlines, lines, 4000, 400)

	// a=620 (stays), b=62 (small, merges forward into c) → b+c = 930
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].LineStart != 1 || segments[0].LineEnd != 20 {
		t.Errorf("seg[0] = %d-%d, want 1-20", segments[0].LineStart, segments[0].LineEnd)
	}
	if segments[1].LineStart != 21 || segments[1].LineEnd != 50 {
		t.Errorf("seg[1] = %d-%d, want 21-50", segments[1].LineStart, segments[1].LineEnd)
	}
}

func TestBuildSegments_MergeTrailingSmall(t *testing.T) {
	// Last segment is small, should merge backward into previous
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 22),
		makeOutline("a", "root", 2, "A", 1, 20),
		makeOutline("b", "root", 2, "B", 21, 22), // tiny, last segment
	}

	lines := make([]string, 22)
	for i := range lines {
		lines[i] = strings.Repeat("字", 30)
	}

	segments := BuildSegments(outlines, lines, 4000, 400)

	// a is big enough, b is small and last → merge backward into a
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment (b merged backward into a), got %d", len(segments))
	}
	if segments[0].LineEnd != 22 {
		t.Errorf("merged segment should end at 22, got %d", segments[0].LineEnd)
	}
}

func TestBuildSegments_SkipTOCSegment(t *testing.T) {
	outlines := []source.Outline{
		makeOutline("root", "", 1, "Root", 1, 40),
		makeOutline("toc", "root", 2, "目录", 1, 10),
		makeOutline("ch1", "root", 2, "Chapter 1", 11, 40),
	}

	lines := make([]string, 40)
	for i := 0; i < 10; i++ {
		lines[i] = "- [第一章](#第一章)"
	}
	for i := 10; i < 40; i++ {
		lines[i] = strings.Repeat("知识内容", 5)
	}

	segments := BuildSegments(outlines, lines, 4000, 400)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment (TOC skipped), got %d", len(segments))
	}
	if segments[0].Title != "Chapter 1" {
		t.Errorf("segment title = %q, want Chapter 1", segments[0].Title)
	}
}

func TestIsMetadataSegment(t *testing.T) {
	tocLines := []string{
		"## 目录",
		"",
		"- [第一章 总则](#第一章-总则)",
		"- [第二章 细则](#第二章-细则)",
		"- [第三章 附则](#第三章-附则)",
	}

	if !isMetadataSegment(tocLines, 1, 5, "目录") {
		t.Error("should detect '目录' title as metadata")
	}
	if !isMetadataSegment(tocLines, 1, 5, "随便") {
		t.Error("should detect >70% toc links as metadata")
	}

	normalLines := []string{
		"知识管理是组织和个人对知识进行系统管理的过程。",
		"它包括知识的获取、存储、共享和应用。",
	}
	if isMetadataSegment(normalLines, 1, 2, "第一章") {
		t.Error("should not flag normal content as metadata")
	}
}

func TestBuildSegments_EmptyOutlines(t *testing.T) {
	segments := BuildSegments(nil, []string{"line1"}, 4000, 400)
	if len(segments) != 0 {
		t.Errorf("expected 0 segments for nil outlines, got %d", len(segments))
	}
}
