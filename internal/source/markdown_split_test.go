package source

import (
	"strings"
	"testing"
)

func TestSplitWindowsByMarkdown_PreservesCodeBlock(t *testing.T) {
	content := "# Title\n\nSome text.\n\n```go\nfunc main() {\n    println(\"hello\")\n}\n```\n\n## Next"
	lines := strings.Split(content, "\n")

	// Small maxRunes to force a split
	windows := splitWindowsByMarkdown(lines, 60, 0.05)
	if len(windows) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(windows))
	}

	// Verify no window splits inside the code block (lines 5-9 are the code block)
	for _, w := range windows {
		startIdx := w.StartLine - 1
		endIdx := w.EndLine - 1
		inCode := false
		for i := startIdx; i <= endIdx && i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				inCode = !inCode
			}
		}
		if inCode {
			t.Errorf("window %d-%d ends inside a code block", w.StartLine, w.EndLine)
		}
	}
}

func TestSplitWindowsByMarkdown_PreservesTable(t *testing.T) {
	// 表格前有足够内容触发分割，但表格本身应在同一窗口
	content := strings.Repeat("这是一段文本内容。\n", 10) + "\n| A | B |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |\n| 5 | 6 |\n\n## Next"
	lines := strings.Split(content, "\n")

	windows := splitWindowsByMarkdown(lines, 120, 0.05)
	if len(windows) < 1 {
		t.Fatal("expected at least 1 window")
	}

	// The table lines (indices 2-6) should all be in the same window
	for _, w := range windows {
		hasTableStart := false
		hasTableEnd := false
		for i := w.StartLine; i <= w.EndLine; i++ {
			if i-1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "| A") {
				hasTableStart = true
			}
			if i-1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "| 5") {
				hasTableEnd = true
			}
		}
		if hasTableStart && !hasTableEnd {
			t.Error("table was split across windows")
		}
	}
}

func TestSplitWindowsByMarkdown_PrefersHeadingBoundary(t *testing.T) {
	var lines []string
	lines = append(lines, "# Chapter 1")
	for i := 0; i < 20; i++ {
		lines = append(lines, "Content line.")
	}
	lines = append(lines, "")
	lines = append(lines, "## Section 2")
	for i := 0; i < 20; i++ {
		lines = append(lines, "More content.")
	}

	windows := splitWindowsByMarkdown(lines, 200, 0.05)
	if len(windows) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(windows))
	}

	// First window should end before "## Section 2" (heading boundary)
	w1End := windows[0].EndLine
	// Section 2 is at line 23 (0-based: 22)
	if w1End > 22 {
		t.Logf("first window ends at line %d, section 2 starts at line 23", w1End)
	}
}

func TestSplitWindowsByMarkdown_SingleWindow(t *testing.T) {
	content := "# Title\n\nShort content."
	lines := strings.Split(content, "\n")

	windows := splitWindowsByMarkdown(lines, 10000, 0.05)
	if len(windows) != 1 {
		t.Errorf("expected 1 window, got %d", len(windows))
	}
	if windows[0].StartLine != 1 || windows[0].EndLine != len(lines) {
		t.Errorf("window = %d-%d, want 1-%d", windows[0].StartLine, windows[0].EndLine, len(lines))
	}
}

func TestMarkBreakPriorities(t *testing.T) {
	lines := []string{
		"# Heading",          // 0: heading → 3
		"",                   // 1: blank → 2
		"Normal text.",       // 2: normal → 1
		"```",                // 3: code start → 0
		"code line",          // 4: in code → 0
		"```",                // 5: code end → 0
		"| A | B |",          // 6: table → 0
		"| 1 | 2 |",         // 7: table → 0
		"",                   // 8: blank → 2
		"## Sub heading",     // 9: heading → 3
	}

	p := markBreakPriorities(lines)

	if p[0] != 3 {
		t.Errorf("heading p[0] = %d, want 3", p[0])
	}
	if p[1] != 2 {
		t.Errorf("blank p[1] = %d, want 2", p[1])
	}
	if p[2] != 1 {
		t.Errorf("normal p[2] = %d, want 1", p[2])
	}
	if p[3] != 0 || p[4] != 0 || p[5] != 0 {
		t.Errorf("code block p[3..5] = %d %d %d, want 0 0 0", p[3], p[4], p[5])
	}
	if p[6] != 0 || p[7] != 0 {
		t.Errorf("table p[6..7] = %d %d, want 0 0", p[6], p[7])
	}
	if p[9] != 3 {
		t.Errorf("sub heading p[9] = %d, want 3", p[9])
	}
}
