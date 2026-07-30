package wiki

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// PagePoints is one published concept page's id and decoded source_point_ids
// — the unit of comparison for page relation derivation (docs/impl/v1/wiki.md
// 步骤 7).
type PagePoints struct {
	PageID   string
	PointIDs []string
}

// ListPublishedConceptPagesWithPoints backs relation derivation: every
// published concept page (topic pages are never a relation-derivation side —
// their inter-page structure is contains, not related/contradicts).
func (s *Store) ListPublishedConceptPagesWithPoints() ([]PagePoints, error) {
	rows, err := s.db.Query(`SELECT page_id, source_point_ids FROM wiki_pages WHERE status = ? AND page_type = ?`,
		StatusPublished, PageTypeConcept)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list published concept pages with points: %w", err)
	}
	defer rows.Close()

	var out []PagePoints
	for rows.Next() {
		var pageID, raw string
		if err := rows.Scan(&pageID, &raw); err != nil {
			return nil, fmt.Errorf("wiki store: scan page points: %w", err)
		}
		var ids []string
		json.Unmarshal([]byte(raw), &ids)
		out = append(out, PagePoints{PageID: pageID, PointIDs: ids})
	}
	return out, rows.Err()
}

// CurrentPointIDs filters ids down to those whose knowledge_points.lifecycle
// is 'current' — page relation derivation must use the same lifecycle filter
// as retrieval (docs/impl/v1/wiki.md 步骤 7 "lifecycle 过滤").
func (s *Store) CurrentPointIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(ids)
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT point_id FROM knowledge_points WHERE lifecycle = 'current' AND point_id IN (%s)`, ph), args...)
	if err != nil {
		return nil, fmt.Errorf("wiki store: current point ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan current point id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountPointRelationsBetween counts knowledge_point_relations rows crossing
// disjoint sets a and b (either direction, since relations are stored
// bidirectionally but as a single row in whatever order the LLM proposed),
// split by relation_type, plus the raw point_id intersection (|a ∩ b|) used
// by the shared_point_min related-derivation condition
// (docs/impl/v1/wiki.md 步骤 7).
func (s *Store) CountPointRelationsBetween(a, b []string) (relatedCount, contradictsCount int, shared []string, err error) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0, nil, nil
	}

	bSet := make(map[string]bool, len(b))
	for _, id := range b {
		bSet[id] = true
	}
	for _, id := range a {
		if bSet[id] {
			shared = append(shared, id)
		}
	}

	phA, argsA := buildPlaceholders(a)
	phB, argsB := buildPlaceholders(b)
	query := fmt.Sprintf(`
		SELECT relation_type, COUNT(*) FROM knowledge_point_relations
		WHERE (source_point_id IN (%s) AND target_point_id IN (%s))
		   OR (source_point_id IN (%s) AND target_point_id IN (%s))
		GROUP BY relation_type`, phA, phB, phB, phA)
	var args []interface{}
	args = append(args, argsA...)
	args = append(args, argsB...)
	args = append(args, argsB...)
	args = append(args, argsA...)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("wiki store: count point relations between: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var relType string
		var count int
		if err := rows.Scan(&relType, &count); err != nil {
			return 0, 0, nil, fmt.Errorf("wiki store: scan relation count: %w", err)
		}
		switch relType {
		case "related":
			relatedCount = count
		case "contradicts":
			contradictsCount = count
		}
	}
	return relatedCount, contradictsCount, shared, rows.Err()
}

// normalizePageOrder implements the undirected relation_type's dictionary
// ordering (docs/impl/v1/wiki.md 步骤 7 "归一化与去重") — from/to always
// sorted so idx_wpr_uniq(from_page_id, to_page_id, relation_type) holds one
// row per pair. contains is directional and must not be normalized —
// callers pass it through UpsertPageRelation directly since only
// related/contradicts go through this package's derivation path.
func normalizePageOrder(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

// UpsertPageRelation writes one wiki_page_relations row. For related/
// contradicts (undirected), from/to are normalized to dictionary order
// before the INSERT so idx_wpr_uniq enforces one row per (pair, type).
// contains (directed, from=topic page) is written as given.
func (s *Store) UpsertPageRelation(fromPageID, toPageID, relationType, derivedFrom, evidenceJSON string) error {
	from, to := fromPageID, toPageID
	if relationType == RelationRelated || relationType == RelationContradicts {
		from, to = normalizePageOrder(fromPageID, toPageID)
	}
	if evidenceJSON == "" {
		evidenceJSON = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO wiki_page_relations (relation_id, from_page_id, to_page_id, relation_type, derived_from, evidence)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_page_id, to_page_id, relation_type)
		DO UPDATE SET derived_from = excluded.derived_from, evidence = excluded.evidence, updated_at = CURRENT_TIMESTAMP`,
		uuid.New().String(), from, to, relationType, derivedFrom, evidenceJSON)
	if err != nil {
		return fmt.Errorf("wiki store: upsert page relation: %w", err)
	}
	return nil
}

// DeletePageRelationsInvolving deletes every row of the given relationTypes
// where pageID is either side.
func (s *Store) DeletePageRelationsInvolving(pageID string, relationTypes ...string) error {
	if len(relationTypes) == 0 {
		return nil
	}
	ph, args := buildPlaceholders(relationTypes)
	query := fmt.Sprintf(
		`DELETE FROM wiki_page_relations WHERE (from_page_id = ? OR to_page_id = ?) AND relation_type IN (%s)`, ph)
	allArgs := append([]interface{}{pageID, pageID}, args...)
	if _, err := s.db.Exec(query, allArgs...); err != nil {
		return fmt.Errorf("wiki store: delete page relations involving: %w", err)
	}
	return nil
}

// ListPageRelations returns every relation row touching pageID, in either
// direction — backs GET /wiki/pages/:id/relations (docs/impl/v1/wiki.md 步骤
// 6).
func (s *Store) ListPageRelations(pageID string) ([]PageRelation, error) {
	rows, err := s.db.Query(`
		SELECT relation_id, from_page_id, to_page_id, relation_type, derived_from, evidence, created_at, updated_at
		FROM wiki_page_relations WHERE from_page_id = ? OR to_page_id = ?`, pageID, pageID)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list page relations: %w", err)
	}
	defer rows.Close()

	var out []PageRelation
	for rows.Next() {
		var r PageRelation
		if err := rows.Scan(&r.RelationID, &r.FromPageID, &r.ToPageID, &r.RelationType, &r.DerivedFrom, &r.Evidence, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("wiki store: scan page relation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRelatedEdges returns every wiki_page_relations related-type row among
// published concept pages — the connected-component graph's edge list for
// topic-page candidate detection (docs/impl/v1/wiki.md 步骤 8).
func (s *Store) ListRelatedEdges() ([]PageRelation, error) {
	rows, err := s.db.Query(`SELECT relation_id, from_page_id, to_page_id, relation_type, derived_from, evidence, created_at, updated_at
		FROM wiki_page_relations WHERE relation_type = ?`, RelationRelated)
	if err != nil {
		return nil, fmt.Errorf("wiki store: list related edges: %w", err)
	}
	defer rows.Close()

	var out []PageRelation
	for rows.Next() {
		var r PageRelation
		if err := rows.Scan(&r.RelationID, &r.FromPageID, &r.ToPageID, &r.RelationType, &r.DerivedFrom, &r.Evidence, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("wiki store: scan related edge: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ContainsMembers returns the contains-row targets (member page ids) for a
// topic page, in insertion order.
func (s *Store) ContainsMembers(topicPageID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT to_page_id FROM wiki_page_relations WHERE from_page_id = ? AND relation_type = ? ORDER BY created_at ASC`,
		topicPageID, RelationContains)
	if err != nil {
		return nil, fmt.Errorf("wiki store: contains members: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan contains member: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ContainingTopics returns the non-archived topic page(s) that contain
// memberPageID — used for cascade recompile (docs/impl/v1/wiki.md 步骤 9).
func (s *Store) ContainingTopics(memberPageID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT wpr.from_page_id FROM wiki_page_relations wpr
		JOIN wiki_pages wp ON wp.page_id = wpr.from_page_id
		WHERE wpr.to_page_id = ? AND wpr.relation_type = ? AND wp.status != ?`,
		memberPageID, RelationContains, StatusArchived)
	if err != nil {
		return nil, fmt.Errorf("wiki store: containing topics: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("wiki store: scan containing topic: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountRelationEdgesWithin counts wiki_page_relations rows of relationType
// whose both endpoints are in pageIDs — used both to size a connected
// component's related-edge count and its contradicts-edge count
// (docs/impl/v1/wiki.md 步骤 8 "候选产生").
func (s *Store) CountRelationEdgesWithin(pageIDs []string, relationType string) (int, error) {
	if len(pageIDs) == 0 {
		return 0, nil
	}
	ph, args := buildPlaceholders(pageIDs)
	var allArgs []interface{}
	allArgs = append(allArgs, relationType)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)
	var count int
	err := s.db.QueryRow(fmt.Sprintf(
		`SELECT COUNT(*) FROM wiki_page_relations WHERE relation_type = ? AND from_page_id IN (%s) AND to_page_id IN (%s)`,
		ph, ph), allArgs...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("wiki store: count relation edges within: %w", err)
	}
	return count, nil
}

// DeleteContainsRow removes one contains edge — used when a topic page's
// recompile drops an archived member (docs/impl/v1/wiki.md 步骤 9).
func (s *Store) DeleteContainsRow(topicPageID, memberPageID string) error {
	_, err := s.db.Exec(`DELETE FROM wiki_page_relations WHERE from_page_id = ? AND to_page_id = ? AND relation_type = ?`,
		topicPageID, memberPageID, RelationContains)
	if err != nil {
		return fmt.Errorf("wiki store: delete contains row: %w", err)
	}
	return nil
}
