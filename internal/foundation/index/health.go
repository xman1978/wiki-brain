package index

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/blevesearch/bleve/v2"
)

type HealthStatus struct {
	Healthy  bool
	Units    IndexHealth
	Points   IndexHealth
	Outlines IndexHealth
	Issues   []string
}

type IndexHealth struct {
	DocCount   uint64
	ExpectedDB int
	Searchable bool
}

// CheckHealth verifies index doc counts match the database and that
// the tokenizer produces searchable results (catches uninitialized gse).
func (m *Manager) CheckHealth(db *sql.DB) HealthStatus {
	status := HealthStatus{Healthy: true}

	status.Units = checkSingle("units", m.Units, db,
		`SELECT COUNT(*) FROM knowledge_units WHERE status='completed'`)
	status.Points = checkSingle("points", m.Points, db,
		`SELECT COUNT(*) FROM knowledge_points kp INNER JOIN knowledge_units ku ON kp.unit_id = ku.unit_id WHERE ku.status='completed'`)
	status.Outlines = checkSingle("outlines", m.Outlines, db,
		`SELECT COUNT(*) FROM source_outlines`)

	for _, pair := range []struct {
		name string
		h    IndexHealth
	}{
		{"units", status.Units},
		{"points", status.Points},
		{"outlines", status.Outlines},
	} {
		if !pair.h.Searchable {
			status.Issues = append(status.Issues, pair.name+" index not searchable (tokenizer may be uninitialized)")
			status.Healthy = false
		}
		if pair.h.ExpectedDB > 0 && pair.h.DocCount == 0 {
			status.Issues = append(status.Issues, fmt.Sprintf("%s index empty but DB has %d rows", pair.name, pair.h.ExpectedDB))
			status.Healthy = false
		}
		if pair.h.ExpectedDB > 0 && pair.h.DocCount > 0 {
			ratio := float64(pair.h.DocCount) / float64(pair.h.ExpectedDB)
			if ratio < 0.9 || ratio > 1.1 {
				status.Issues = append(status.Issues, fmt.Sprintf("%s count mismatch: index=%d db=%d", pair.name, pair.h.DocCount, pair.h.ExpectedDB))
				status.Healthy = false
			}
		}
	}

	return status
}

func checkSingle(name string, idx bleve.Index, db *sql.DB, countQuery string) IndexHealth {
	h := IndexHealth{}

	docCount, err := idx.DocCount()
	if err != nil {
		slog.Warn("health: doc count failed", "index", name, "error", err)
		return h
	}
	h.DocCount = docCount

	if err := db.QueryRow(countQuery).Scan(&h.ExpectedDB); err != nil {
		slog.Warn("health: db count failed", "index", name, "error", err)
	}

	h.Searchable = probeSearchable(idx)
	return h
}

// probeSearchable indexes a probe document with Chinese text, searches for it,
// then removes it. This catches the common failure mode where gse segmenter
// is not initialized — indexing succeeds but produces zero tokens, so search
// returns no results.
func probeSearchable(idx bleve.Index) bool {
	const probeID = "__health_probe__"
	const probeText = "健康检查探针"

	if err := idx.Index(probeID, map[string]interface{}{"content": probeText}); err != nil {
		return false
	}
	defer idx.Delete(probeID)

	q := bleve.NewMatchQuery(probeText)
	req := bleve.NewSearchRequest(q)
	req.Size = 1
	res, err := idx.Search(req)
	if err != nil {
		return false
	}
	return res.Total > 0
}

// EnsureHealthy opens or creates indexes, checks health against the database,
// and rebuilds from scratch if unhealthy. It guarantees that InitSegmenter has
// been called before any index operation.
func EnsureHealthy(idxPath string, db *sql.DB, readMarkdown func(sourceID string) ([]string, error)) (*Manager, error) {
	if err := InitSegmenter(); err != nil {
		return nil, fmt.Errorf("index: init segmenter: %w", err)
	}

	mgr, err := NewManager(idxPath)
	if err != nil {
		slog.Warn("index open failed, rebuilding from scratch", "error", err)
		os.RemoveAll(idxPath)
		mgr, err = NewManager(idxPath)
		if err != nil {
			return nil, fmt.Errorf("index: create after cleanup: %w", err)
		}
	}

	status := mgr.CheckHealth(db)
	if status.Healthy {
		slog.Info("index health check passed",
			"units", status.Units.DocCount,
			"points", status.Points.DocCount,
			"outlines", status.Outlines.DocCount)
		return mgr, nil
	}

	slog.Warn("index unhealthy, rebuilding", "issues", status.Issues)

	mgr.Close()
	os.RemoveAll(idxPath)

	mgr, err = NewManager(idxPath)
	if err != nil {
		return nil, fmt.Errorf("index: recreate: %w", err)
	}

	stats, err := mgr.Rebuild(db, readMarkdown)
	if err != nil {
		mgr.Close()
		return nil, fmt.Errorf("index: rebuild: %w", err)
	}

	slog.Info("index rebuilt", "units", stats.Units, "points", stats.Points, "outlines", stats.Outlines)
	return mgr, nil
}
