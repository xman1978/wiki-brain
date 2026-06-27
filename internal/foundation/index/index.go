package index

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blevesearch/bleve/v2"
	_ "github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	_ "github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/mapping"
)

type Manager struct {
	Units    bleve.Index
	Points   bleve.Index
	Outlines bleve.Index
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
