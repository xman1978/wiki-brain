package source

import (
	"strings"
	"testing"
)

func TestExtractStructuralOutlines_Basic(t *testing.T) {
	content := `# Title

Some intro text.

## Section 1

Content of section 1.

## Section 2

Content of section 2.

### Subsection 2.1

Sub content.`

	outlines := ExtractStructuralOutlines("src-1", content)
	if len(outlines) != 4 {
		t.Fatalf("got %d outlines, want 4", len(outlines))
	}

	// Check first outline
	if outlines[0].Title != "Title" || outlines[0].Level != 1 {
		t.Errorf("outline[0]: title=%q level=%d", outlines[0].Title, outlines[0].Level)
	}
	if outlines[0].LineStart != 1 {
		t.Errorf("outline[0].LineStart = %d, want 1", outlines[0].LineStart)
	}
	if outlines[0].ParentID.Valid {
		t.Error("root node should not have parent")
	}
	if outlines[0].Position != 0 {
		t.Errorf("outline[0].Position = %d, want 0", outlines[0].Position)
	}

	// Section 1 should be child of Title
	if !outlines[1].ParentID.Valid || outlines[1].ParentID.String != outlines[0].OutlineID {
		t.Error("Section 1 should have Title as parent")
	}

	// Subsection 2.1 should be child of Section 2
	if !outlines[3].ParentID.Valid || outlines[3].ParentID.String != outlines[2].OutlineID {
		t.Error("Subsection 2.1 should have Section 2 as parent")
	}
}

func TestExtractStructuralOutlines_NoHeadings(t *testing.T) {
	content := "Just some plain text\nwith no headings."
	outlines := ExtractStructuralOutlines("src-1", content)
	if len(outlines) != 0 {
		t.Errorf("got %d outlines, want 0", len(outlines))
	}
}

func TestExtractStructuralOutlines_LineEndCoversDocument(t *testing.T) {
	content := "# Only heading\n\nSome content\nMore content"
	outlines := ExtractStructuralOutlines("src-1", content)
	if len(outlines) != 1 {
		t.Fatalf("got %d, want 1", len(outlines))
	}
	totalLines := len(strings.Split(content, "\n"))
	if outlines[0].LineEnd != totalLines {
		t.Errorf("LineEnd = %d, want %d (total lines)", outlines[0].LineEnd, totalLines)
	}
}

func TestExtractStructuralOutlines_CodeBlockIgnored(t *testing.T) {
	content := "# Real heading\n\n```\n## Not a heading\n```\n\nMore text"
	outlines := ExtractStructuralOutlines("src-1", content)
	if len(outlines) != 1 {
		t.Errorf("got %d outlines, want 1 (code block heading should be ignored)", len(outlines))
	}
}

func TestCheckSemanticTrigger_ConditionA(t *testing.T) {
	result := CheckSemanticTrigger(nil, "some content", "markdown", 4000)
	if !result.Triggered {
		t.Error("should trigger on no outlines")
	}
	if len(result.Reasons) == 0 || !strings.HasPrefix(result.Reasons[0], "A:") {
		t.Errorf("reasons = %v, want A:", result.Reasons)
	}
}

func TestCheckSemanticTrigger_ConditionB(t *testing.T) {
	longContent := strings.Repeat("这是一段中文内容。", 300)
	content := "# Heading\n\n" + longContent
	outlines := ExtractStructuralOutlines("src-1", content)
	// Only 1 outline, content > 2000 runes
	result := CheckSemanticTrigger(outlines, content, "markdown", 100000)
	if !result.Triggered {
		t.Error("should trigger: too sparse")
	}
}

func TestCheckSemanticTrigger_NoTrigger(t *testing.T) {
	content := "# H1\n\n## S1\n\ntext\n\n## S2\n\ntext\n\n## S3\n\ntext\n\n## S4\n\ntext\n\n## S5\n\ntext"
	outlines := ExtractStructuralOutlines("src-1", content)
	result := CheckSemanticTrigger(outlines, content, "markdown", 100000)
	if result.Triggered {
		t.Errorf("should not trigger, reasons: %v", result.Reasons)
	}
}

func TestCheckSemanticTrigger_ConditionD_Flat(t *testing.T) {
	content := "## A\n\ntext\n\n## B\n\ntext\n\n## C\n\ntext"
	outlines := ExtractStructuralOutlines("src-1", content)
	result := CheckSemanticTrigger(outlines, content, "markdown", 100000)
	if !result.Triggered {
		t.Error("should trigger: flat structure with < 5 nodes")
	}
}

func TestCheckSemanticTrigger_ConditionE_LongLeaf(t *testing.T) {
	longText := strings.Repeat("这是很长的内容。", 600) // ~4800 runes
	content := "# H1\n\n## S1\n\n" + longText + "\n\n## S2\n\nshort\n\n## S3\n\nshort\n\n## S4\n\nshort\n\n## S5\n\nshort"
	outlines := ExtractStructuralOutlines("src-1", content)
	result := CheckSemanticTrigger(outlines, content, "markdown", 4000)
	if !result.Triggered {
		t.Error("should trigger: leaf node too long")
	}
}
