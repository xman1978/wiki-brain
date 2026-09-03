package foundation

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

type presetData struct {
	Domains []presetDomain `json:"domains"`
}

type presetDomain struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Concepts      []presetEntry       `json:"entries"`
	DocCategories []presetDocCategory `json:"doc_categories"`
}

// presetDocCategory is one doc_categories row (docs/design/doc-category.md):
// a predefined document-genre label scoped to this domain, curated by hand
// like entries but with no candidate/evolution machinery — value domain is
// closed and domain-specific, refreshed at preset load, never auto-mined.
type presetDocCategory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type presetEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	// Boundary clarifies what this entry does/does not cover — written
	// specifically to disambiguate near-neighbor entries during
	// unit_entry_match. Previously authored in domains.json but never
	// parsed or persisted; now stored on entries.boundary (migration 044)
	// and surfaced to that matching prompt.
	Boundary string `json:"boundary"`
	// Kind is the concept/fact classification (docs/impl/v1/kpn.md 步骤 3).
	// Empty defaults to "concept" (matches entries.kind's column default);
	// anything else is normalized the same way at preset-load time.
	Kind string `json:"kind"`
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
			kind := c.Kind
			switch kind {
			case "", "concept":
				kind = "concept"
			case "fact":
				kind = "fact"
			default:
				slog.Warn("preset entry has unrecognized kind, defaulting to concept", "entry_id", c.ID, "kind", c.Kind)
				kind = "concept"
			}
			aliasesJSON, err := json.Marshal(c.Aliases)
			if err != nil {
				return err
			}
			var boundary sql.NullString
			if c.Boundary != "" {
				boundary = sql.NullString{String: c.Boundary, Valid: true}
			}

			// Real UPSERT (not INSERT OR IGNORE): re-running preset should
			// refresh name/description/boundary/aliases/kind, but must never
			// clear merged_into or rewrite origin — a merged-away concept
			// doesn't get revived by still existing in domains.json, and a
			// human-confirmed evolved concept isn't reclassified back to
			// preset just because preset happens to define the same entry_id
			// (docs/impl/v1/concept-evolution.md 步骤 4). boundary/aliases/kind
			// are preset-authored curation data, same trust level as
			// name/description, so they refresh unconditionally like those
			// two already did.
			_, err = tx.Exec(
				`INSERT INTO entries (entry_id, domain_id, name, description, boundary, aliases, kind) VALUES (?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(entry_id) DO UPDATE SET name = excluded.name, description = excluded.description,
				   boundary = excluded.boundary, aliases = excluded.aliases, kind = excluded.kind`,
				c.ID, d.ID, c.Name, c.Description, boundary, string(aliasesJSON), kind,
			)
			if err != nil {
				return err
			}

			if err := upsertEntryAliasSynonyms(tx, d.ID, c); err != nil {
				return err
			}
		}

		for _, dc := range d.DocCategories {
			_, err := tx.Exec(
				`INSERT INTO doc_categories (category_id, domain_id, name, description) VALUES (?, ?, ?, ?)
				 ON CONFLICT(category_id) DO UPDATE SET name = excluded.name, description = excluded.description,
				   domain_id = excluded.domain_id, updated_at = CURRENT_TIMESTAMP`,
				dc.ID, d.ID, dc.Name, dc.Description,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// upsertEntryAliasSynonyms feeds a concept's preset aliases into
// subject_synonyms as a free, already-curated starting dictionary for
// ActivationLink Match's subject-dimension synonym canonicalization
// (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
// Each alias canonicalizes to the concept's own name. Re-running preset
// refreshes canonical/domain_id for rows it created (source='preset'), but
// never touches a term that a gap-mined or manually confirmed row already
// owns (source != 'preset') — preset replay must not silently override a
// human decision made after the fact.
func upsertEntryAliasSynonyms(tx *sql.Tx, domainID string, c presetEntry) error {
	canonical := text.Normalize(c.Name)
	if canonical == "" {
		return nil
	}
	for _, alias := range c.Aliases {
		term := text.Normalize(alias)
		if term == "" || term == canonical {
			continue
		}

		var existingID, existingSource string
		err := tx.QueryRow(
			`SELECT synonym_id, source FROM subject_synonyms WHERE term = ? AND status = 'active'`,
			term,
		).Scan(&existingID, &existingSource)

		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(
				`INSERT INTO subject_synonyms (synonym_id, domain_id, term, canonical, source, status)
				 VALUES (?, ?, ?, ?, 'preset', 'active')`,
				uuid.New().String(), domainID, term, canonical,
			); err != nil {
				return err
			}
		case err != nil:
			return err
		case existingSource == "preset":
			if _, err := tx.Exec(
				`UPDATE subject_synonyms SET canonical = ?, domain_id = ?, updated_at = CURRENT_TIMESTAMP
				 WHERE synonym_id = ?`,
				canonical, domainID, existingID,
			); err != nil {
				return err
			}
		default:
			// A gap_mined/manual row already owns this term while active —
			// leave it alone (see function doc).
		}
	}
	return nil
}
