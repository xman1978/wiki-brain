//go:build integration

package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
)

func TestE2E_SemanticOutlineWithRealLLM(t *testing.T) {
	cfg, err := config.Load("../../config/config.yml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, "../../config/prompts")
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()
	for _, dir := range []string{
		"data/sources/original",
		"data/sources/html",
		"data/sources/markdown",
	} {
		os.MkdirAll(filepath.Join(tmpDir, dir), 0755)
	}

	store := NewStore(db)
	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "data", "searchindex"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	fv := NewFileViewClient(cfg.FileView.BaseURL, cfg.FileView.PollIntervalMs, cfg.FileView.MaxPollSeconds)
	q := queue.New(100)

	svc := NewService(store, fv, llmClient, idxMgr.Outlines, q, cfg, tmpDir)

	// 用 d16dd42e.md 测试（无标题结构，触发语义 outline）
	testFile := "../../test/d16dd42e.md"
	file, err := os.Open(testFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	src, err := svc.Import(context.Background(), "d16dd42e.md", file)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if err := svc.Process(context.Background(), src.SourceID); err != nil {
		t.Fatalf("Process: %v", err)
	}

	got, _ := store.GetByID(src.SourceID)
	t.Logf("status=%s outline_type=%v word_count=%v", got.Status, got.OutlineType, got.WordCount)
	if got.Summary.Valid {
		t.Logf("summary: %s", got.Summary.String)
	}

	if got.Status != "completed" {
		t.Fatalf("status=%s error=%v", got.Status, got.ErrorMsg)
	}
	if !got.OutlineType.Valid || got.OutlineType.String != "semantic" {
		t.Errorf("outline_type=%v, want semantic", got.OutlineType)
	}

	tree, _ := store.GetOutlineTree(src.SourceID)
	printTree(t, tree, 0)

	outlines, _ := store.GetOutlines(src.SourceID)
	md, _ := svc.GetMarkdown(src.SourceID)
	lines := strings.Split(md, "\n")
	totalLines := len(lines)

	for _, o := range outlines {
		if o.LineStart < 1 || o.LineEnd > totalLines || o.LineStart > o.LineEnd {
			t.Errorf("invalid range: %q line %d-%d (total %d)", o.Title, o.LineStart, o.LineEnd, totalLines)
		}
	}

	if len(outlines) < 2 {
		t.Errorf("expected at least 2 outline nodes, got %d", len(outlines))
	}
}

func printTree(t *testing.T, tree []OutlineTree, depth int) {
	for _, n := range tree {
		indent := strings.Repeat("  ", depth)
		summary := ""
		if n.Summary != nil {
			summary = fmt.Sprintf(" [%s]", *n.Summary)
		}
		t.Logf("%s- L%d %s (line %d-%d, %s)%s", indent, n.Level, n.Title, n.LineStart, n.LineEnd, n.NodeType, summary)
		printTree(t, n.Children, depth+1)
	}
}
