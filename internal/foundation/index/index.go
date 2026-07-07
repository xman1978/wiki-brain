package index

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blevesearch/bleve/v2"
	_ "github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	_ "github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/mapping"
)

type Manager struct {
	Units    bleve.Index
	Points   bleve.Index
	Outlines bleve.Index
	Wiki     bleve.Index
	basePath string
}

func NewManager(basePath string) (*Manager, error) {
	if err := ensureSegmenter(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("index: create dir %s: %w", basePath, err)
	}

	m := &Manager{basePath: basePath}

	var err error
	m.Units, err = openOrCreate(filepath.Join(basePath, "units"))
	if err != nil {
		return nil, fmt.Errorf("index: units: %w", err)
	}

	m.Points, err = openOrCreate(filepath.Join(basePath, "points"))
	if err != nil {
		m.Units.Close()
		return nil, fmt.Errorf("index: points: %w", err)
	}

	m.Outlines, err = openOrCreate(filepath.Join(basePath, "outlines"))
	if err != nil {
		m.Units.Close()
		m.Points.Close()
		return nil, fmt.Errorf("index: outlines: %w", err)
	}

	m.Wiki, err = openOrCreate(filepath.Join(basePath, "wiki"))
	if err != nil {
		m.Units.Close()
		m.Points.Close()
		m.Outlines.Close()
		return nil, fmt.Errorf("index: wiki: %w", err)
	}

	return m, nil
}

func (m *Manager) Close() error {
	var firstErr error
	if err := m.Units.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := m.Points.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := m.Outlines.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := m.Wiki.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func newIndexMapping() (*mapping.IndexMappingImpl, error) {
	analyzerDef := map[string]interface{}{
		"type":      "custom",
		"tokenizer": "gse",
		"token_filters": []interface{}{
			"to_lower",
		},
	}

	m := bleve.NewIndexMapping()
	if err := m.AddCustomAnalyzer("wiki_brain", analyzerDef); err != nil {
		return nil, fmt.Errorf("register wiki_brain analyzer: %w", err)
	}
	m.DefaultAnalyzer = "wiki_brain"

	// lifecycle 是精确值（current/superseded/deprecated），检索层用
	// TermQuery(lifecycle=current) 做 conjunction 过滤（docs/impl/v1/lifecycle.md
	// 步骤3："units / points 索引写入字段增加 lifecycle（keyword，不分词）"）。
	// 不加此字段映射会走 DefaultAnalyzer（gse 分词），索引出的词项和裸值不一致，
	// TermQuery 永远查不到。
	lifecycleField := bleve.NewTextFieldMapping()
	lifecycleField.Analyzer = keyword.Name

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("lifecycle", lifecycleField)
	m.DefaultMapping = docMapping

	return m, nil
}

func openOrCreate(path string) (bleve.Index, error) {
	idx, err := bleve.Open(path)
	if err == nil {
		return idx, nil
	}

	m, err := newIndexMapping()
	if err != nil {
		return nil, err
	}

	idx, err = bleve.New(path, m)
	if err != nil {
		return nil, fmt.Errorf("create index at %s: %w", path, err)
	}
	return idx, nil
}
