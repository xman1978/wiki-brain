//go:build integration

package source

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func runSemanticOnlyTest(t *testing.T, fileName string) {
	t.Helper()

	cfg, err := config.Load("../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, "../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	raw, err := os.ReadFile("../../test/" + fileName)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := NormalizeMarkdown(string(raw))
	lines := strings.Split(content, "\n")
	totalRunes := RuneCount(content)

	mc := cfg.LLM.ModelForPurpose("extraction")
	t.Logf("file=%s lines=%d runes=%d max_input_tokens=%d", fileName, len(lines), totalRunes, mc.MaxInputTokens)

	outlines, err := GenerateSemanticOutlines(
		context.Background(), llmClient, "test-semantic",
		content, mc, cfg.Source.SegmentMaxChars,
	)
	if err != nil {
		t.Fatalf("GenerateSemanticOutlines: %v", err)
	}

	t.Logf("outlines: %d total", len(outlines))

	tree := buildOutlineTree(outlines)
	printTree(t, tree, 0)

	for _, o := range outlines {
		if o.LineStart < 1 || o.LineEnd > len(lines) || o.LineStart > o.LineEnd {
			t.Errorf("invalid range: %q line %d-%d (total %d)", o.Title, o.LineStart, o.LineEnd, len(lines))
		}
	}

	leaves := findLeafNodes(outlines)
	oversize := 0
	for _, leaf := range leaves {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		runes := RuneCount(nodeContent)
		if runes > cfg.Source.SegmentMaxChars {
			oversize++
			t.Errorf("leaf %q (%d-%d) has %d runes > segment_max_chars=%d",
				leaf.Title, leaf.LineStart, leaf.LineEnd, runes, cfg.Source.SegmentMaxChars)
		}
	}
	t.Logf("leaves: %d total, %d oversize", len(leaves), oversize)

	// 统计各层级节点数
	levelCount := make(map[int]int)
	for _, o := range outlines {
		levelCount[o.Level]++
	}
	for l := 1; l <= 3; l++ {
		if c, ok := levelCount[l]; ok {
			t.Logf("  L%d: %d nodes", l, c)
		}
	}
}

func TestE2E_SemanticOnlyLargestFile(t *testing.T) {
	runSemanticOnlyTest(t, "44c2483c.md")
}

func TestE2E_SemanticOnly_LeafRefinement(t *testing.T) {
	runSemanticOnlyTest(t, "4a7f9d51.md")
}

func outlinesToFlat(outlines []Outline) []Outline {
	return outlines
}

func printLeafSizes(t *testing.T, outlines []Outline, lines []string) {
	leaves := findLeafNodes(outlines)
	for _, leaf := range leaves {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		runes := RuneCount(nodeContent)
		t.Logf("  leaf %q (%d-%d): %d runes", leaf.Title, leaf.LineStart, leaf.LineEnd, runes)
	}
}
