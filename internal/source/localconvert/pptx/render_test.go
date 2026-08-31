package pptx

import (
	"strings"
	"testing"
)

func TestRenderBasicDeck(t *testing.T) {
	deck, err := ExtractFile("testdata/fixtures/basic.pptx")
	if err != nil {
		t.Fatal(err)
	}
	md := ToMarkdown(deck)
	for _, w := range []string{
		"# Quarterly Review",
		"## Slide 1: Quarterly Review",
		"- Revenue up 12%",
		"[IMAGE: Architecture diagram]",
		"> **Notes:** Mention Europe performance first.",
		"## Slide 2: Roadmap",
		"---",
	} {
		if !strings.Contains(md, w) {
			t.Fatalf("missing %q in:\n%s", w, md)
		}
	}
	for _, nw := range []string{"![", "media/", "### Speaker notes"} {
		if strings.Contains(md, nw) {
			t.Fatalf("unexpected %q in:\n%s", nw, md)
		}
	}
}

func TestRenderTable(t *testing.T) {
	deck, err := ExtractFile("testdata/fixtures/table.pptx")
	if err != nil {
		t.Fatal(err)
	}
	md := ToMarkdown(deck)
	for _, w := range []string{"| Area | Status |", "| --- | --- |", "| Parser | Done |"} {
		if !strings.Contains(md, w) {
			t.Fatalf("missing %q in:\n%s", w, md)
		}
	}
}

func TestRenderImagePlaceholderEmptyAlt(t *testing.T) {
	deck := Deck{Title: "T", Slides: []Slide{{Number: 1, Title: "S", Blocks: []Block{{Type: "image"}}}}}
	md := ToMarkdown(deck)
	if !strings.Contains(md, "[IMAGE: no description]") {
		t.Fatalf("missing fallback alt in:\n%s", md)
	}
}

func TestRenderEmptyTableOmitted(t *testing.T) {
	deck := Deck{Title: "T", Slides: []Slide{{Number: 1, Title: "S", Blocks: []Block{
		{Type: "table", Rows: [][]string{{"", ""}, {"", ""}, {"", ""}}},
	}}}}
	md := ToMarkdown(deck)
	if strings.Contains(md, "|") {
		t.Fatalf("all-empty table should render nothing, got:\n%s", md)
	}
}

func TestRenderTableWithSomeContentKept(t *testing.T) {
	deck := Deck{Title: "T", Slides: []Slide{{Number: 1, Title: "S", Blocks: []Block{
		{Type: "table", Rows: [][]string{{"Area", "Status"}, {"Parser", ""}}},
	}}}}
	md := ToMarkdown(deck)
	if !strings.Contains(md, "| Area | Status |") || !strings.Contains(md, "| Parser |  |") {
		t.Fatalf("table with partial content should still render, got:\n%s", md)
	}
}

func TestRenderNestedBullets(t *testing.T) {
	deck := Deck{Title: "T", Slides: []Slide{{Number: 1, Title: "S", Blocks: []Block{
		{Type: "bullet", Text: "top", Level: 0},
		{Type: "bullet", Text: "child", Level: 1},
	}}}}
	md := ToMarkdown(deck)
	if !strings.Contains(md, "- top\n  - child") {
		t.Fatalf("nested bullets wrong in:\n%s", md)
	}
}
