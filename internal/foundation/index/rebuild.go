package index

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type RebuildStats struct {
	Units    int
	Points   int
	Outlines int
}

func (m *Manager) Rebuild(db *sql.DB, readMarkdown func(sourceID string) ([]string, error)) (RebuildStats, error) {
	var stats RebuildStats

	n, err := m.rebuildUnits(db, readMarkdown)
	if err != nil {
		return stats, fmt.Errorf("rebuild units index: %w", err)
	}
	stats.Units = n

	n, err = m.rebuildPoints(db)
	if err != nil {
		return stats, fmt.Errorf("rebuild points index: %w", err)
	}
	stats.Points = n

	n, err = m.rebuildOutlines(db)
	if err != nil {
		return stats, fmt.Errorf("rebuild outlines index: %w", err)
	}
	stats.Outlines = n

	return stats, nil
}

func (m *Manager) rebuildUnits(db *sql.DB, readMarkdown func(sourceID string) ([]string, error)) (int, error) {
	rows, err := db.Query(`SELECT ku.unit_id, ku.source_id, ku.center, ku.line_start, ku.line_end
		FROM knowledge_units ku WHERE ku.status = 'completed'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	batch := m.Units.NewBatch()
	count := 0
	mdCache := make(map[string][]string)

	for rows.Next() {
		var unitID, sourceID, center string
		var lineStart, lineEnd int
		if err := rows.Scan(&unitID, &sourceID, &center, &lineStart, &lineEnd); err != nil {
			slog.Warn("rebuild: scan unit failed", "error", err)
			continue
		}

		lines, ok := mdCache[sourceID]
		if !ok {
			lines, err = readMarkdown(sourceID)
			if err != nil {
				slog.Warn("rebuild: read markdown failed", "source_id", sourceID, "error", err)
				continue
			}
			mdCache[sourceID] = lines
		}

		content := sliceLines(lines, lineStart, lineEnd)

		batch.Index(unitID, map[string]interface{}{
			"unit_id":   unitID,
			"source_id": sourceID,
			"center":    center,
			"line_start": lineStart,
			"line_end":   lineEnd,
			"content":   content,
		})
		count++

		if count%100 == 0 {
			if err := m.Units.Batch(batch); err != nil {
				return 0, err
			}
			batch = m.Units.NewBatch()
		}
	}

	if batch.Size() > 0 {
		if err := m.Units.Batch(batch); err != nil {
			return 0, err
		}
	}

	return count, rows.Err()
}

func (m *Manager) rebuildPoints(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type
		FROM knowledge_points kp
		INNER JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.status = 'completed'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	batch := m.Points.NewBatch()
	count := 0

	for rows.Next() {
		var pointID, unitID, sourceID, content, pointType string
		if err := rows.Scan(&pointID, &unitID, &sourceID, &content, &pointType); err != nil {
			slog.Warn("rebuild: scan point failed", "error", err)
			continue
		}

		batch.Index(pointID, map[string]interface{}{
			"point_id":   pointID,
			"unit_id":    unitID,
			"source_id":  sourceID,
			"content":    content,
			"point_type": pointType,
		})
		count++

		if count%100 == 0 {
			if err := m.Points.Batch(batch); err != nil {
				return 0, err
			}
			batch = m.Points.NewBatch()
		}
	}

	if batch.Size() > 0 {
		if err := m.Points.Batch(batch); err != nil {
			return 0, err
		}
	}

	return count, rows.Err()
}

func (m *Manager) rebuildOutlines(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT outline_id, source_id, title, summary, level, node_type
		FROM source_outlines`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	batch := m.Outlines.NewBatch()
	count := 0

	for rows.Next() {
		var outlineID, sourceID, title, nodeType string
		var summary sql.NullString
		var level int
		if err := rows.Scan(&outlineID, &sourceID, &title, &summary, &level, &nodeType); err != nil {
			slog.Warn("rebuild: scan outline failed", "error", err)
			continue
		}

		doc := map[string]interface{}{
			"outline_id": outlineID,
			"source_id":  sourceID,
			"title":      title,
			"level":      level,
			"node_type":  nodeType,
		}
		if summary.Valid {
			doc["summary"] = summary.String
		}

		batch.Index(outlineID, doc)
		count++

		if count%100 == 0 {
			if err := m.Outlines.Batch(batch); err != nil {
				return 0, err
			}
			batch = m.Outlines.NewBatch()
		}
	}

	if batch.Size() > 0 {
		if err := m.Outlines.Batch(batch); err != nil {
			return 0, err
		}
	}

	return count, rows.Err()
}

func RebuildFromScratch(dbPath, idxPath string, readMarkdown func(sourceID string) ([]string, error)) (RebuildStats, error) {
	os.RemoveAll(idxPath)

	mgr, err := NewManager(idxPath)
	if err != nil {
		return RebuildStats{}, fmt.Errorf("create index manager: %w", err)
	}
	defer mgr.Close()

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return RebuildStats{}, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	return mgr.Rebuild(db, readMarkdown)
}

func sliceLines(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	if lineStart > lineEnd {
		return ""
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}
