package source

import (
	"context"
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

func TestRefineLeafNodes(t *testing.T) {
	fake := llm.NewFakeClient()
	fake.SetResponse("outline_semantic_chunk.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"Sub A","summary":"sub a keys","line_start":1,"line_end":5,"level":2},
			{"title":"Sub B","summary":"sub b keys","line_start":6,"line_end":10,"level":2}
		]}`,
	})

	longContent := strings.Repeat("这是一段很长的中文内容用于测试。\n", 600)
	content := "# Heading\n" + longContent

	existingOutlines := ExtractStructuralOutlines("src-1", content)
	newOutlines, err := RefineLeafNodes(context.Background(), fake, "src-1", content, existingOutlines, testExtractionMC(100000), 100)
	if err != nil {
		t.Fatalf("RefineLeafNodes: %v", err)
	}
	if len(newOutlines) == 0 {
		t.Error("expected refined leaf nodes")
	}
	for _, o := range newOutlines {
		if o.NodeType != "semantic" {
			t.Errorf("refined node type = %q, want semantic", o.NodeType)
		}
	}
}

func TestDeduplicateOverlap(t *testing.T) {
	outlines := []Outline{
		{OutlineID: "a", Title: "Intro", LineStart: 1, LineEnd: 10},
		{OutlineID: "b", Title: "Intro", LineStart: 5, LineEnd: 12},
		{OutlineID: "c", Title: "Body", LineStart: 13, LineEnd: 20},
	}
	result := deduplicateOverlap(outlines)
	if len(result) != 2 {
		t.Fatalf("got %d, want 2", len(result))
	}
	if result[0].LineEnd != 12 {
		t.Errorf("merged LineEnd = %d, want 12", result[0].LineEnd)
	}
}
