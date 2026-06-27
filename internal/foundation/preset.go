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
			_, err := tx.Exec(
				"INSERT OR IGNORE INTO concepts (concept_id, domain_id, name, description) VALUES (?, ?, ?, ?)",
				c.ID, d.ID, c.Name, c.Description,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
