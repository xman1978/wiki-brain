package pptx

import "testing"

func TestPostprocessCollapsesBlankLines(t *testing.T) {
	got := PostprocessText("a\n\n\n\nb\n")
	if got != "a\n\nb\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPostprocessTrimsTrailingWhitespace(t *testing.T) {
	got := PostprocessText("a   \nb\t\n")
	if got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPostprocessNormalizesCRLF(t *testing.T) {
	got := PostprocessText("a\r\nb\r\n")
	if got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
}
