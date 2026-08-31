package mcp

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/source"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"第三章 差旅报销", "第三章-差旅报销"},
		{"Expense Policy!!", "expense-policy"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"", ""},
	}
	for _, c := range cases {
		if got := slugify(c.title); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestCitationResolverResolve(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := source.NewStore(db)

	src := &source.Source{
		Title:        "报销制度",
		Format:       "markdown",
		FileName:     "expense.md",
		OriginalPath: "data/sources/original/expense.md",
		MarkdownPath: "/data/sources/markdown/expense.md",
		Status:       "completed",
	}
	if err := store.Create(src); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.InsertOutlines([]source.Outline{
		{SourceID: src.SourceID, Level: 1, Title: "第一章 总则", LineStart: 1, LineEnd: 10, NodeType: "heading"},
		{SourceID: src.SourceID, Level: 1, Title: "第三章 差旅报销", LineStart: 11, LineEnd: 40, NodeType: "heading"},
	}); err != nil {
		t.Fatalf("InsertOutlines: %v", err)
	}

	resolver := newCitationResolver(store)

	got := resolver.resolve(src.SourceID, 25)
	if got.SourceTitle != "报销制度" {
		t.Errorf("SourceTitle = %q, want 报销制度", got.SourceTitle)
	}
	if got.Section != "第三章 差旅报销" {
		t.Errorf("Section = %q, want 第三章 差旅报销", got.Section)
	}
	want := "file:///data/sources/markdown/expense.md#第三章-差旅报销"
	if got.Link != want {
		t.Errorf("Link = %q, want %q", got.Link, want)
	}

	// line outside every outline node's range: no section, no anchor.
	noSection := resolver.resolve(src.SourceID, 999)
	if noSection.Section != "" {
		t.Errorf("Section = %q, want empty", noSection.Section)
	}
	if noSection.Link != "file:///data/sources/markdown/expense.md" {
		t.Errorf("Link = %q, want unanchored path", noSection.Link)
	}

	// unknown source_id degrades to an empty Citation, not an error.
	empty := resolver.resolve("does-not-exist", 1)
	if empty != (Citation{}) {
		t.Errorf("Citation for unknown source = %+v, want zero value", empty)
	}
}
