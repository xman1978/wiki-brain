package domain

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// List returns every domain with its concept/source/KP/pending-signal counts,
// for the 知识领域 page's left rail. Counts use correlated subqueries rather
// than a join+GROUP BY: the domains table is small and this keeps each count's
// own filter (merged_into, shadow_of, lifecycle) independent and readable.
func (s *Store) List() ([]Domain, error) {
	rows, err := s.db.Query(`
		SELECT
			d.domain_id, d.name, COALESCE(d.description, ''), d.created_at,
			(SELECT COUNT(*) FROM entries c
			 WHERE c.domain_id = d.domain_id AND c.merged_into IS NULL) AS entry_count,
			(SELECT COUNT(*) FROM sources s
			 WHERE s.domain_id = d.domain_id AND s.shadow_of IS NULL) AS source_count,
			(SELECT COUNT(*) FROM knowledge_points kp
			 JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
			 JOIN entries c2 ON c2.entry_id = ku.entry_id
			 WHERE c2.domain_id = d.domain_id AND kp.lifecycle = 'current' AND ku.lifecycle = 'current') AS kp_count,
			(SELECT COUNT(*) FROM knowledge_points kp
			 JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
			 JOIN sources s ON s.source_id = kp.source_id
			 WHERE ku.entry_id IS NULL AND ku.lifecycle = 'current' AND kp.lifecycle = 'current'
			   AND s.domain_id = d.domain_id AND s.shadow_of IS NULL) AS unassigned_kp_count,
			(SELECT COUNT(*) FROM entry_candidates cc
			 WHERE cc.domain_id = d.domain_id AND cc.status = 'pending_confirm') AS pending_signal_count
		FROM domains d
		ORDER BY d.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("domain store: list: %w", err)
	}
	defer rows.Close()

	var results []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.DomainID, &d.Name, &d.Description, &d.CreatedAt,
			&d.ConceptCount, &d.SourceCount, &d.KPCount, &d.UnassignedKPCount, &d.PendingSignalCount); err != nil {
			return nil, fmt.Errorf("domain store: scan: %w", err)
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

// Create inserts a new domain and returns its generated id.
func (s *Store) Create(name, description string) (string, error) {
	domainID := uuid.New().String()
	_, err := s.db.Exec(
		`INSERT INTO domains (domain_id, name, description) VALUES (?, ?, ?)`,
		domainID, name, description,
	)
	if err != nil {
		return "", fmt.Errorf("domain store: create: %w", err)
	}
	return domainID, nil
}

// Update edits an existing domain's name/description in place.
func (s *Store) Update(domainID, name, description string) error {
	res, err := s.db.Exec(
		`UPDATE domains SET name = ?, description = ? WHERE domain_id = ?`,
		name, description, domainID,
	)
	if err != nil {
		return fmt.Errorf("domain store: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("domain store: update rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListDocCategories returns domainID's document-category value domain
// (docs/design/doc-category.md), each with how many non-shadow sources
// currently carry it.
func (s *Store) ListDocCategories(domainID string) ([]DocCategory, error) {
	rows, err := s.db.Query(`
		SELECT
			dc.category_id, dc.domain_id, dc.name, COALESCE(dc.description, ''), dc.created_at,
			(SELECT COUNT(*) FROM sources s
			 WHERE s.doc_category_id = dc.category_id AND s.shadow_of IS NULL) AS source_count
		FROM doc_categories dc
		WHERE dc.domain_id = ?
		ORDER BY dc.name ASC`, domainID)
	if err != nil {
		return nil, fmt.Errorf("domain store: list doc categories: %w", err)
	}
	defer rows.Close()

	var results []DocCategory
	for rows.Next() {
		var dc DocCategory
		if err := rows.Scan(&dc.CategoryID, &dc.DomainID, &dc.Name, &dc.Description, &dc.CreatedAt, &dc.SourceCount); err != nil {
			return nil, fmt.Errorf("domain store: scan doc category: %w", err)
		}
		results = append(results, dc)
	}
	return results, rows.Err()
}

// CreateDocCategory inserts a new doc_categories row and returns its
// generated id.
func (s *Store) CreateDocCategory(domainID, name, description string) (string, error) {
	categoryID := uuid.New().String()
	_, err := s.db.Exec(
		`INSERT INTO doc_categories (category_id, domain_id, name, description) VALUES (?, ?, ?, ?)`,
		categoryID, domainID, name, description,
	)
	if err != nil {
		return "", fmt.Errorf("domain store: create doc category: %w", err)
	}
	return categoryID, nil
}

// UpdateDocCategory edits an existing category's name/description in place.
func (s *Store) UpdateDocCategory(categoryID, name, description string) error {
	res, err := s.db.Exec(
		`UPDATE doc_categories SET name = ?, description = ?, updated_at = CURRENT_TIMESTAMP WHERE category_id = ?`,
		name, description, categoryID,
	)
	if err != nil {
		return fmt.Errorf("domain store: update doc category: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("domain store: update doc category rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteDocCategory removes a category, clearing (not cascading) the
// doc_category_id of any source still carrying it — deleting a category
// unclassifies its documents, it doesn't delete them.
func (s *Store) DeleteDocCategory(categoryID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("domain store: delete doc category: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE sources SET doc_category_id = NULL WHERE doc_category_id = ?`, categoryID); err != nil {
		return fmt.Errorf("domain store: delete doc category: clear sources: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM doc_categories WHERE category_id = ?`, categoryID); err != nil {
		return fmt.Errorf("domain store: delete doc category: %w", err)
	}
	return tx.Commit()
}
