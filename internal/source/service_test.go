package source

import (
	"context"
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

func setupTestService(t *testing.T) (*Service, *llm.FakeClient) {
	t.Helper()
	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()

	// Create required dirs
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
	}

	svc := NewService(store, nil, fake, idxMgr.Outlines, q, cfg, tmpDir)
	return svc, fake
}

func TestServiceProcess_MarkdownSource(t *testing.T) {
	svc, fake := setupTestService(t)

	fake.SetResponse("source_summary.md", llm.FakeResponse{
		Output: "这是一份关于Go编程的技术文档",
	})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{
		Output: `{"domain_id": null}`,
	})

	// Create a markdown source file
	mdContent := `# Go Programming

## Variables

Variables store values.

## Functions

Functions encapsulate logic.

## Concurrency

Go has goroutines.

## Error Handling

Handle errors properly.

## Testing

Write tests for code.
`
	sourceID := "test-src-1"
	mdDir := filepath.Join(svc.baseDir, "data", "sources", "markdown")
	os.WriteFile(filepath.Join(mdDir, sourceID+".md"), []byte(mdContent), 0644)
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".md"), []byte(mdContent), 0644)

	src := &Source{
		SourceID:     sourceID,
		Title:        "Go Programming",
		Format:       "markdown",
		FileName:     "go.md",
		OriginalPath: "data/sources/original/" + sourceID + ".md",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md",
		Status:       "pending",
	}
	if err := svc.store.Create(src); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Process(context.Background(), sourceID); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Verify status
	got, _ := svc.store.GetByID(sourceID)
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if !got.OutlineType.Valid || got.OutlineType.String != "structural" {
		t.Errorf("outline_type = %v, want structural", got.OutlineType)
	}
	if !got.Summary.Valid {
		t.Error("summary should be set")
	}
	if !got.WordCount.Valid || got.WordCount.Int64 == 0 {
		t.Error("word_count should be set")
	}

	// Verify outlines
	outlines, _ := svc.store.GetOutlines(sourceID)
	if len(outlines) == 0 {
		t.Error("should have outlines")
	}

	// Verify tree structure
	tree, _ := svc.store.GetOutlineTree(sourceID)
	if len(tree) == 0 {
		t.Error("should have outline tree")
	}
}

func TestServiceProcess_SemanticTrigger(t *testing.T) {
	svc, fake := setupTestService(t)

	fake.SetResponse("outline_semantic_full.md", llm.FakeResponse{
		Output: `{"sections":[
			{"title":"Introduction","summary":"intro overview","line_start":1,"line_end":5,"level":1},
			{"title":"Main Content","summary":"main body","line_start":6,"line_end":10,"level":1}
		]}`,
	})
	fake.SetResponse("source_summary.md", llm.FakeResponse{
		Output: "一份纯文本文档",
	})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{
		Output: `{"domain_id": null}`,
	})

	// No headings → trigger condition A
	content := strings.Repeat("这是一段普通文本。\n", 10)
	sourceID := "test-src-2"
	mdDir := filepath.Join(svc.baseDir, "data", "sources", "markdown")
	os.WriteFile(filepath.Join(mdDir, sourceID+".md"), []byte(content), 0644)
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".txt"), []byte(content), 0644)

	src := &Source{
		SourceID:     sourceID,
		Title:        "Plain Text",
		Format:       "other",
		FileName:     "plain.txt",
		OriginalPath: "data/sources/original/" + sourceID + ".txt",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md",
		Status:       "pending",
	}
	svc.store.Create(src)

	if err := svc.Process(context.Background(), sourceID); err != nil {
		t.Fatalf("Process: %v", err)
	}

	got, _ := svc.store.GetByID(sourceID)
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if !got.OutlineType.Valid || got.OutlineType.String != "semantic" {
		t.Errorf("outline_type = %v, want semantic", got.OutlineType)
	}
}

func TestExtractFirstParagraph(t *testing.T) {
	content := "# Heading\n\nFirst paragraph here.\n\nSecond paragraph."
	got := extractFirstParagraph(content, 300)
	if strings.Contains(got, "# Heading") {
		t.Error("should skip headings")
	}
	if !strings.Contains(got, "First paragraph") {
		t.Error("should include first paragraph")
	}
}

func TestGenerateSummary_Fallback(t *testing.T) {
	svc, fake := setupTestService(t)

	// LLM fails
	fake.SetResponse("source_summary.md", llm.FakeResponse{
		Err: llm.ErrModelError,
	})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{
		Output: `{"domain_id": null}`,
	})

	sourceID := "test-src-3"
	content := "# Heading\n\nSome content"
	mdDir := filepath.Join(svc.baseDir, "data", "sources", "markdown")
	os.WriteFile(filepath.Join(mdDir, sourceID+".md"), []byte(content), 0644)
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".md"), []byte(content), 0644)

	src := &Source{
		SourceID:     sourceID,
		Title:        "Test",
		Format:       "markdown",
		FileName:     "test.md",
		OriginalPath: "data/sources/original/" + sourceID + ".md",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md",
		Status:       "pending",
	}
	svc.store.Create(src)
	svc.Process(context.Background(), sourceID)

	// Should still complete, summary comes from fallback
	got, _ := svc.store.GetByID(sourceID)
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
}
