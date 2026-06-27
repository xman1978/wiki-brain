package source

import (
	"strings"
	"testing"
)

func TestNormalizeMarkdown_CompressBlankLines(t *testing.T) {
	input := "line1\n\n\n\n\nline2"
	got := NormalizeMarkdown(input)
	if strings.Count(got, "\n\n") > 1 {
		t.Errorf("expected single blank line between paragraphs, got:\n%q", got)
	}
}

func TestNormalizeMarkdown_RemoveCarriageReturn(t *testing.T) {
	input := "line1\r\nline2\rline3"
	got := NormalizeMarkdown(input)
	if strings.Contains(got, "\r") {
		t.Error("should not contain \\r")
	}
}

func TestNormalizeMarkdown_RemoveHTMLComment(t *testing.T) {
	input := "before<!-- comment -->after"
	got := NormalizeMarkdown(input)
	if strings.Contains(got, "<!--") {
		t.Error("should remove HTML comments")
	}
	if got != "beforeafter" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeMarkdown_RemoveZeroWidth(t *testing.T) {
	input := "hello​world"
	got := NormalizeMarkdown(input)
	if got != "helloworld" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeMarkdown_DemoteH5ToH4(t *testing.T) {
	input := "##### Deep heading"
	got := NormalizeMarkdown(input)
	if !strings.HasPrefix(got, "#### ") {
		t.Errorf("expected H4, got: %q", got)
	}
}

func TestNormalizeMarkdown_H6DemoteToH4(t *testing.T) {
	input := "###### Very deep"
	got := NormalizeMarkdown(input)
	if !strings.HasPrefix(got, "#### ") {
		t.Errorf("expected H4, got: %q", got)
	}
}

func TestNormalizeMarkdown_PreserveCodeBlock(t *testing.T) {
	input := "```go\n##### not a heading\n```"
	got := NormalizeMarkdown(input)
	if !strings.Contains(got, "##### not a heading") {
		t.Error("should preserve content inside code blocks")
	}
}

func TestNormalizeMarkdown_InsertVirtualHeading(t *testing.T) {
	input := "# Title\n\n### Section"
	got := NormalizeMarkdown(input)
	lines := strings.Split(got, "\n")
	foundH2 := false
	for _, l := range lines {
		if strings.HasPrefix(l, "## ") {
			foundH2 = true
		}
	}
	if !foundH2 {
		t.Errorf("expected virtual H2 to be inserted, got:\n%s", got)
	}
}

func TestNormalizeMarkdown_NoVirtualWhenContinuous(t *testing.T) {
	input := "# Title\n\n## Section\n\n### Sub"
	got := NormalizeMarkdown(input)
	if got != input {
		t.Errorf("should not modify continuous levels:\n%s", got)
	}
}

func TestRuneCount(t *testing.T) {
	if n := RuneCount("你好世界"); n != 4 {
		t.Errorf("RuneCount = %d, want 4", n)
	}
}
