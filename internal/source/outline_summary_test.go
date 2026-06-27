package source

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestGenerateOutlineSummaries_SingleBatch(t *testing.T) {
	fake := llm.NewFakeClient()
	content := "# Title\n\nSome content here.\n\n## Section A\n\nDetails of A.\n\n## Section B\n\nDetails of B."

	outlines := ExtractStructuralOutlines("src-1", content)
	if len(outlines) == 0 {
		t.Fatal("no outlines")
	}

	// Build response matching outline IDs
	respJSON := `{"summaries":[`
	for i, o := range outlines {
		if i > 0 {
			respJSON += ","
		}
		respJSON += `{"id":"` + o.OutlineID + `","summary":"关键词A 关键词B 关键词C"}`
	}
	respJSON += `]}`

	fake.SetResponse("outline_summary.md", llm.FakeResponse{Output: respJSON})

	mc := config.ModelConfig{
		Model:           "test",
		MaxInputTokens:  100000,
		MaxOutputTokens: 4096,
	}

	GenerateOutlineSummaries(context.Background(), fake, outlines, content, mc)

	for _, o := range outlines {
		if !o.Summary.Valid || o.Summary.String == "" {
			t.Errorf("outline %q has no summary", o.Title)
		}
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Errorf("expected 1 LLM call (single batch), got %d", len(calls))
	}
}

func TestGenerateOutlineSummaries_MultipleBatches(t *testing.T) {
	fake := llm.NewFakeClient()

	// Create content with many sections
	var lines []string
	lines = append(lines, "# Doc Title")
	for i := 0; i < 20; i++ {
		lines = append(lines, "")
		lines = append(lines, "## Section "+string(rune('A'+i)))
		for j := 0; j < 10; j++ {
			lines = append(lines, "这是一段很长的中文内容用于填充，确保每个节点有足够的文本进行分析和提取关键词。")
		}
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}

	outlines := ExtractStructuralOutlines("src-1", content)

	// Set up fake to always return valid response
	fake.SetResponse("outline_summary.md", llm.FakeResponse{
		Output: `{"summaries":[{"id":"placeholder","summary":"kw1 kw2 kw3"}]}`,
	})

	// Very small max_input_tokens to force multiple batches
	mc := config.ModelConfig{
		Model:           "test",
		MaxInputTokens:  200,
		MaxOutputTokens: 4096,
	}

	GenerateOutlineSummaries(context.Background(), fake, outlines, content, mc)

	calls := fake.Calls()
	if len(calls) < 2 {
		t.Errorf("expected multiple batches, got %d calls", len(calls))
	}
}

func TestGenerateOutlineSummaries_SkipsExistingSummary(t *testing.T) {
	fake := llm.NewFakeClient()
	content := "# Title\n\nContent"

	outlines := ExtractStructuralOutlines("src-1", content)
	// Pre-fill summary
	for i := range outlines {
		outlines[i].Summary.Valid = true
		outlines[i].Summary.String = "existing keywords"
	}

	mc := config.ModelConfig{Model: "test", MaxInputTokens: 100000, MaxOutputTokens: 4096}
	GenerateOutlineSummaries(context.Background(), fake, outlines, content, mc)

	// Should not call LLM since all already have summaries
	if len(fake.Calls()) != 0 {
		t.Errorf("should not call LLM when all outlines have summaries, got %d calls", len(fake.Calls()))
	}
}
