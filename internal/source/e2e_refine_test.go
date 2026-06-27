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

func TestE2E_SemanticWithLeafRefinement(t *testing.T) {
	cfg, err := config.Load("../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, "../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	raw, err := os.ReadFile("../../test/44c2483c.md")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := NormalizeMarkdown(string(raw))
	lines := strings.Split(content, "\n")

	mc := cfg.LLM.ModelForPurpose("extraction")
	// 用小的 segment_max_chars 强制触发叶节点细化
	smallSegmentMax := 2000
	t.Logf("file=44c2483c.md lines=%d runes=%d segment_max_chars=%d", len(lines), RuneCount(content), smallSegmentMax)

	outlines, err := GenerateSemanticOutlines(
		context.Background(), llmClient, "test-refine",
		content, mc, smallSegmentMax,
	)
	if err != nil {
		t.Fatalf("GenerateSemanticOutlines: %v", err)
	}

	t.Logf("outlines: %d total", len(outlines))

	tree := buildOutlineTree(outlines)
	printTree(t, tree, 0)

	// 统计层级
	levelCount := make(map[int]int)
	for _, o := range outlines {
		levelCount[o.Level]++
	}
	for l := 1; l <= 3; l++ {
		if c, ok := levelCount[l]; ok {
			t.Logf("  L%d: %d nodes", l, c)
		}
	}

	if levelCount[2] == 0 {
		t.Error("expected L2 nodes from leaf refinement, but got none")
	}

	// 验证叶节点大小
	leaves := findLeafNodes(outlines)
	oversize := 0
	for _, leaf := range leaves {
		nodeContent := extractLineRange(lines, leaf.LineStart, leaf.LineEnd)
		runes := RuneCount(nodeContent)
		if runes > smallSegmentMax {
			oversize++
			t.Logf("WARNING: leaf %q (%d-%d) has %d runes > %d", leaf.Title, leaf.LineStart, leaf.LineEnd, runes, smallSegmentMax)
		}
	}
	t.Logf("leaves: %d total, %d oversize", len(leaves), oversize)
}
