package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

func TestMain(m *testing.M) {
	if err := InitSegmenter(
		"../../../config/dict/it.txt",
		"../../../config/dict/finance.txt",
		"../../../config/dict/wiki_brain.txt",
	); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestNewManager(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	if mgr.Units == nil || mgr.Points == nil || mgr.Outlines == nil {
		t.Fatal("one or more indexes are nil")
	}

	for _, name := range []string{"units", "points", "outlines"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("index dir %s not created: %v", name, err)
		}
	}
}

func TestReopenExistingIndex(t *testing.T) {
	dir := t.TempDir()

	mgr1, err := NewManager(dir)
	if err != nil {
		t.Fatalf("first NewManager: %v", err)
	}

	if err := mgr1.Units.Index("doc1", map[string]interface{}{"content": "测试内容"}); err != nil {
		t.Fatalf("index doc: %v", err)
	}
	mgr1.Close()

	mgr2, err := NewManager(dir)
	if err != nil {
		t.Fatalf("second NewManager: %v", err)
	}
	defer mgr2.Close()

	count, err := mgr2.Units.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if count != 1 {
		t.Errorf("doc count = %d, want 1", count)
	}
}

func TestChineseSearch(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	docs := map[string]string{
		"1": "知识单元是系统的基本组成部分",
		"2": "深度学习在自然语言处理中的应用",
		"3": "数据库索引优化方案",
	}

	for id, content := range docs {
		if err := mgr.Units.Index(id, map[string]interface{}{"content": content}); err != nil {
			t.Fatalf("index %s: %v", id, err)
		}
	}

	query := bleve.NewMatchQuery("知识单元")
	query.Analyzer = "wiki_brain"
	req := bleve.NewSearchRequest(query)
	result, err := mgr.Units.Search(req)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected at least one result for '知识单元'")
	}
	if result.Total > 0 && result.Hits[0].ID != "1" {
		t.Errorf("top hit = %q, want '1'", result.Hits[0].ID)
	}
}

func TestIndexAndDelete(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	if err := mgr.Units.Index("x", map[string]interface{}{"content": "test"}); err != nil {
		t.Fatal(err)
	}

	count, _ := mgr.Units.DocCount()
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	if err := mgr.Units.Delete("x"); err != nil {
		t.Fatal(err)
	}

	count, _ = mgr.Units.DocCount()
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}
