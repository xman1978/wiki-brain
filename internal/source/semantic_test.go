package source

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func testExtractionMC(maxInput int) config.ModelConfig {
	return config.ModelConfig{
		Model:           "test",
		Temperature:     0,
		MaxInputTokens:  maxInput,
		MaxOutputTokens: 4096,
	}
}

func TestSinglePassOutline(t *testing.T) {
	fake := llm.NewFakeClient()
	fake.SetResponse("outline_semantic_full.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"Introduction","summary":"intro overview","line_start":1,"line_end":5,"level":1},
			{"title":"Details","summary":"detail content","line_start":6,"line_end":10,"level":1}
		]}`,
	})

	content := strings.Repeat("line\n", 10)
	// Large max_input_tokens so single pass is used
	outlines, err := GenerateSemanticOutlines(context.Background(), fake, "src-1", content, testExtractionMC(100000), 4000)
	if err != nil {
		t.Fatalf("GenerateSemanticOutlines: %v", err)
	}
	if len(outlines) != 2 {
		t.Fatalf("got %d outlines, want 2", len(outlines))
	}
	if outlines[0].NodeType != "semantic" {
		t.Errorf("node_type = %q, want semantic", outlines[0].NodeType)
	}
	if outlines[0].Position != 0 || outlines[1].Position != 1 {
		t.Errorf("positions: %d, %d", outlines[0].Position, outlines[1].Position)
	}
}

func TestHierarchicalCompression(t *testing.T) {
	fake := llm.NewFakeClient()

	// Local sketch response
	fake.SetResponse("outline_local_sketch.md", llm.FakeResponse{
		Output: `{"outline_units":[
			{"title":"Part 1","summary":"first part","line_start":1,"line_end":50,"level_hint":1},
			{"title":"Part 2","summary":"second part","line_start":51,"line_end":100,"level_hint":1}
		],"starts_mid_section":false,"ends_mid_section":false,"start_topic":null,"end_topic":null}`,
	})

	// Global merge response
	fake.SetResponse("outline_global_merge.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"Part 1","summary":"first part","line_start":1,"line_end":50,"level":1},
			{"title":"Part 2","summary":"second part","line_start":51,"line_end":100,"level":1}
		]}`,
	})

	// Generate enough content to exceed single pass with small max_input_tokens
	content := strings.Repeat("这是一段用于测试分窗口提取的中文文本内容。\n", 100)
	// max_input_tokens=20 → usable≈15 tokens → ~22 runes, content is ~2200 runes → multi-window
	outlines, err := GenerateSemanticOutlines(context.Background(), fake, "src-1", content, testExtractionMC(20), 4000)
	if err != nil {
		t.Fatalf("GenerateSemanticOutlines: %v", err)
	}
	if len(outlines) < 2 {
		t.Fatalf("got %d outlines, want at least 2", len(outlines))
	}
}

func TestParseSectionsToOutlines_ParentChild(t *testing.T) {
	data := []byte(`{"sections":[
		{"title":"Ch1","summary":"k1","line_start":1,"line_end":20,"level":1},
		{"title":"Sec1.1","summary":"k2","line_start":1,"line_end":10,"level":2},
		{"title":"Sec1.2","summary":"k3","line_start":11,"line_end":20,"level":2}
	]}`)

	outlines, err := parseSectionsToOutlines(data, "src-1", 20, "semantic")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(outlines) != 3 {
		t.Fatalf("got %d, want 3", len(outlines))
	}

	if outlines[0].ParentID.Valid {
		t.Error("Ch1 should be root")
	}
	if !outlines[1].ParentID.Valid || outlines[1].ParentID.String != outlines[0].OutlineID {
		t.Error("Sec1.1 should be child of Ch1")
	}
	if !outlines[2].ParentID.Valid || outlines[2].ParentID.String != outlines[0].OutlineID {
		t.Error("Sec1.2 should be child of Ch1")
	}
}

// TestRefineLeafNodes_ProducesNonOverlappingFullCoverage is the regression
// test for the 差旅费报销制度 incident: the model used to be asked for
// line_start/line_end/level directly and would return overlapping sections
// (e.g. a broad section wholly containing several narrower ones), which
// internal/unit's mergeSmallSegments had no defense against — it merged
// unrelated later chapters into one mislabeled extraction batch, and those
// chapters ended up with zero knowledge units. RefineLeafNodes now delegates
// to splitOversizeLeaf, which decides boundaries deterministically and only
// asks the model for a title per already-fixed block, so overlap is
// structurally impossible regardless of what the model returns.
func TestRefineLeafNodes_ProducesNonOverlappingFullCoverage(t *testing.T) {
	fake := llm.NewFakeClient()
	var titles strings.Builder
	titles.WriteString(`{"titles":[`)
	for i := 1; i <= 20; i++ {
		if i > 1 {
			titles.WriteString(",")
		}
		fmt.Fprintf(&titles, `{"index":%d,"title":"标题%d"}`, i, i)
	}
	titles.WriteString(`]}`)
	fake.SetResponse("outline_semantic_chunk.md", llm.FakeResponse{Output: titles.String()})

	var b strings.Builder
	b.WriteString("# Heading\n\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "条款%d：这是条款正文内容，重复凑字数重复凑字数重复凑字数重复凑字数。\n\n", i)
	}
	b.WriteString("| A | B |\n| --- | --- |\n")
	for i := 0; i < 20; i++ {
		b.WriteString("| row | value that is reasonably long to add up characters |\n")
	}
	content := b.String()

	existingOutlines := ExtractStructuralOutlines("src-1", content)
	leaves := findLeafNodes(existingOutlines)
	if len(leaves) != 1 {
		t.Fatalf("expected exactly 1 structural leaf to set up the test, got %d", len(leaves))
	}
	leaf := leaves[0]

	newOutlines, err := RefineLeafNodes(context.Background(), fake, "src-1", content, existingOutlines, testExtractionMC(100000), 150)
	if err != nil {
		t.Fatalf("RefineLeafNodes: %v", err)
	}
	if len(newOutlines) < 2 {
		t.Fatalf("expected multiple refined children, got %d", len(newOutlines))
	}

	sort.Slice(newOutlines, func(i, j int) bool { return newOutlines[i].LineStart < newOutlines[j].LineStart })

	if newOutlines[0].LineStart != leaf.LineStart {
		t.Errorf("first child LineStart = %d, want %d (leaf's own start)", newOutlines[0].LineStart, leaf.LineStart)
	}
	if newOutlines[len(newOutlines)-1].LineEnd != leaf.LineEnd {
		t.Errorf("last child LineEnd = %d, want %d (leaf's own end)", newOutlines[len(newOutlines)-1].LineEnd, leaf.LineEnd)
	}
	for i := 1; i < len(newOutlines); i++ {
		if newOutlines[i].LineStart != newOutlines[i-1].LineEnd+1 {
			t.Errorf("child %d (starts %d) is not immediately after child %d (ends %d) — gap or overlap",
				i, newOutlines[i].LineStart, i-1, newOutlines[i-1].LineEnd)
		}
	}

	lines := strings.Split(content, "\n")
	tableStartLine, tableLastLine := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "| A") {
			tableStartLine = i + 1
		}
		if strings.HasPrefix(strings.TrimSpace(l), "| row") {
			tableLastLine = i + 1
		}
	}
	foundTableChild := false
	for _, o := range newOutlines {
		if o.LineStart <= tableStartLine && o.LineEnd >= tableStartLine {
			if o.LineEnd < tableLastLine {
				t.Error("table was split across children despite avoidCuttingIndivisible")
			}
			foundTableChild = true
		}
	}
	if !foundTableChild {
		t.Fatal("no child covers the table's start line")
	}

	for _, o := range newOutlines {
		if o.NodeType != "semantic" {
			t.Errorf("refined node type = %q, want semantic", o.NodeType)
		}
		if strings.TrimSpace(o.Title) == "" {
			t.Error("child has empty title")
		}
	}
}
