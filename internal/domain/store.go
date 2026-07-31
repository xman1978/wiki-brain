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
			(SELECT COUNT(*) FROM concepts c
			 WHERE c.domain_id = d.domain_id AND c.merged_into IS NULL) AS concept_count,
			(SELECT COUNT(*) FROM sources s
			 WHERE s.domain_id = d.domain_id AND s.shadow_of IS NULL) AS source_count,
			(SELECT COUNT(*) FROM knowledge_points kp
			 JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
			 JOIN concepts c2 ON c2.concept_id = ku.concept_id
			 WHERE c2.domain_id = d.domain_id AND kp.lifecycle = 'current' AND ku.lifecycle = 'current') AS kp_count,
			(SELECT COUNT(*) FROM concept_candidates cc
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
			&d.ConceptCount, &d.SourceCount, &d.KPCount, &d.PendingSignalCount); err != nil {
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
