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

func setupIntegrationService(t *testing.T) (*Service, *llm.FakeClient) {
	t.Helper()
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
	fake := llm.NewFakeClient()

	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "data", "searchindex"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	q := queue.New(100)
	cfg := &config.Config{
		Source: config.SourceConfig{
			SegmentMaxChars: 4000,
			MinSegmentChars: 400,
		},
		LLM: config.LLMConfig{
			TimeoutSeconds: 30,
			MaxRetries:     0,
			Models: map[string]config.ModelConfig{
				"default":    {Model: "test", MaxInputTokens: 100000, MaxOutputTokens: 4096},
				"extraction": {Model: "test", MaxInputTokens: 100000, MaxOutputTokens: 4096},
			},
		},
		FileView: config.FileViewConfig{
			BaseURL:        "http://127.0.0.1:8000",
			PollIntervalMs: 1500,
			MaxPollSeconds: 600,
		},
	}

	fv := NewFileViewClient(cfg.FileView.BaseURL, cfg.FileView.PollIntervalMs, cfg.FileView.MaxPollSeconds)
	svc := NewService(store, fv, fake, idxMgr.Outlines, q, cfg, tmpDir)
	return svc, fake
}

func TestIntegration_ProcessMarkdownFiles(t *testing.T) {
	svc, fake := setupIntegrationService(t)

	fake.SetResponse("source_summary.md", llm.FakeResponse{
		Output: "这是一份企业内部管理文档",
	})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{
		Output: `{"domain_id": null}`,
	})
	fake.SetResponse("outline_semantic_full.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"概述","summary":"通知 制度 规定","line_start":1,"line_end":10,"level":1},
			{"title":"细则","summary":"执行 规范 要求","line_start":11,"line_end":20,"level":1}
		]}`,
	})
	fake.SetResponse("outline_semantic_skeleton.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"概述","summary":"通知 制度 规定","line_start":1,"line_end":10,"level":1},
			{"title":"细则","summary":"执行 规范 要求","line_start":11,"line_end":20,"level":1}
		]}`,
	})
	fake.SetResponse("outline_semantic_chunk.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"子章节A","summary":"细节 要点 说明","line_start":1,"line_end":15,"level":3},
			{"title":"子章节B","summary":"补充 内容 总结","line_start":16,"line_end":30,"level":3}
		]}`,
	})

	testDir := filepath.Join("..", "..", "test")
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("read test dir: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			srcPath := filepath.Join(testDir, entry.Name())
			file, err := os.Open(srcPath)
			if err != nil {
				t.Fatalf("open file: %v", err)
			}
			defer file.Close()

			src, err := svc.Import(context.Background(), entry.Name(), file)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}

			if err := svc.Process(context.Background(), src.SourceID); err != nil {
				t.Fatalf("Process: %v", err)
			}

			got, err := svc.store.GetByID(src.SourceID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}

			if got.Status != "completed" {
				t.Errorf("status = %q, want completed (error: %v)", got.Status, got.ErrorMsg)
			}

			// Verify markdown was written and normalized
			md, err := svc.GetMarkdown(src.SourceID)
			if err != nil {
				t.Fatalf("GetMarkdown: %v", err)
			}
			if len(md) == 0 {
				t.Error("markdown is empty")
			}
			if strings.Contains(md, "\r") {
				t.Error("markdown still contains \\r")
			}
			if strings.Contains(md, "\n\n\n") {
				t.Error("markdown has triple blank lines")
			}

			// Verify outlines
			outlines, err := svc.store.GetOutlines(src.SourceID)
			if err != nil {
				t.Fatalf("GetOutlines: %v", err)
			}

			// Verify word count
			if !got.WordCount.Valid || got.WordCount.Int64 == 0 {
				t.Error("word_count not set")
			}

			// Verify outline line ranges are valid
			lines := strings.Split(md, "\n")
			totalLines := len(lines)
			for _, o := range outlines {
				if o.LineStart < 1 || o.LineStart > totalLines {
					t.Errorf("outline %q: line_start=%d out of range (total=%d)", o.Title, o.LineStart, totalLines)
				}
				if o.LineEnd < o.LineStart || o.LineEnd > totalLines {
					t.Errorf("outline %q: line_end=%d out of range (start=%d, total=%d)", o.Title, o.LineEnd, o.LineStart, totalLines)
				}
			}

			// Log summary for review
			fmt.Printf("  %-15s status=%-9s outline_type=%-10v outlines=%d words=%d\n",
				entry.Name(), got.Status,
				got.OutlineType, len(outlines), got.WordCount.Int64)
		})
	}
}
