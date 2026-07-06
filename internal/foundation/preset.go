package foundation

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
)

type presetData struct {
	Domains []presetDomain `json:"domains"`
}

type presetDomain struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Concepts    []presetConcept `json:"concepts"`
}

type presetConcept struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func LoadPresetData(db *sql.DB, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		slog.Warn("preset data file not found, skipping", "path", filePath, "error", err)
		return nil
	}

	var preset presetData
	if err := json.Unmarshal(data, &preset); err != nil {
		slog.Warn("preset data parse failed, skipping", "path", filePath, "error", err)
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, d := range preset.Domains {
		_, err := tx.Exec(
			"INSERT OR IGNORE INTO domains (domain_id, name, description) VALUES (?, ?, ?)",
			d.ID, d.Name, d.Description,
		)
		if err != nil {
			return err
		}

		for _, c := range d.Concepts {
			// Real UPSERT (not INSERT OR IGNORE): re-running preset should
			// refresh name/description, but must never clear merged_into or
			// rewrite origin — a merged-away concept doesn't get revived by
			// still existing in domains.json, and a human-confirmed evolved
			// concept isn't reclassified back to preset just because preset
			// happens to define the same concept_id
			// (docs/impl/v1/concept-evolution.md 步骤 4).
			_, err := tx.Exec(
				`INSERT INTO concepts (concept_id, domain_id, name, description) VALUES (?, ?, ?, ?)
				 ON CONFLICT(concept_id) DO UPDATE SET name = excluded.name, description = excluded.description`,
				c.ID, d.ID, c.Name, c.Description,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
