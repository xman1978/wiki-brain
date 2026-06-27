//go:build integration

package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
)

func TestE2E_StructuralOutlineWithSummary(t *testing.T) {
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
	for _, dir := range []string{"data/sources/original", "data/sources/html", "data/sources/markdown"} {
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

	// 19803e65.md — 有 H1+H2 结构的文档
	testFile := "../../test/19803e65.md"
	file, err := os.Open(testFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	src, err := svc.Import(context.Background(), "19803e65.md", file)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if err := svc.Process(context.Background(), src.SourceID); err != nil {
		t.Fatalf("Process: %v", err)
	}

	got, _ := store.GetByID(src.SourceID)
	t.Logf("status=%s outline_type=%v", got.Status, got.OutlineType)

	if got.Status != "completed" {
		t.Fatalf("status=%s error=%v", got.Status, got.ErrorMsg)
	}

	tree, _ := store.GetOutlineTree(src.SourceID)
	printTree(t, tree, 0)

	// 验证结构节点都有 summary
	outlines, _ := store.GetOutlines(src.SourceID)
	withSummary := 0
	for _, o := range outlines {
		if o.Summary.Valid && o.Summary.String != "" {
			withSummary++
		}
	}
	t.Logf("outlines: %d total, %d with summary", len(outlines), withSummary)

	if withSummary == 0 {
		t.Error("expected structural outlines to have summaries")
	}
}
