package retrieval

import (
	"database/sql"
	"fmt"
	"strings"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

type DomainInfo struct {
	DomainID    string
	Name        string
	Description string
}

type SourceInfo struct {
	SourceID     string
	Title        string
	Summary      sql.NullString
	DomainID     sql.NullString
	MarkdownPath string
}

type OutlineInfo struct {
	OutlineID string
	SourceID  string
	ParentID  sql.NullString
	Level     int
	Title     string
	Summary   sql.NullString
	LineStart int
	LineEnd   int
}

type UnitInfo struct {
	UnitID    string
	SourceID  string
	OutlineID sql.NullString
	LineStart int
	LineEnd   int
}

func (s *Store) ListDomains() ([]DomainInfo, error) {
	rows, err := s.db.Query(`SELECT domain_id, name, COALESCE(description, '') FROM domains ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list domains: %w", err)
	}
	defer rows.Close()

	var result []DomainInfo
	for rows.Next() {
		var d DomainInfo
		if err := rows.Scan(&d.DomainID, &d.Name, &d.Description); err != nil {
			return nil, fmt.Errorf("retrieval store: scan domain: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) ListSourcesByDomainIDs(domainIDs []string) ([]SourceInfo, error) {
	if len(domainIDs) == 0 {
		return s.ListAllSources()
	}
	placeholders := make([]string, len(domainIDs))
	args := make([]interface{}, len(domainIDs))
	for i, id := range domainIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT source_id, title, summary, domain_id, markdown_path FROM sources
		 WHERE domain_id IN (%s) OR domain_id IS NULL
		 ORDER BY created_at DESC`,
		strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list sources by domains: %w", err)
	}
	defer rows.Close()
	return scanSources(rows)
}

func (s *Store) ListAllSources() ([]SourceInfo, error) {
	rows, err := s.db.Query(`SELECT source_id, title, summary, domain_id, markdown_path FROM sources ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: list all sources: %w", err)
	}
	defer rows.Close()
	return scanSources(rows)
}

func scanSources(rows *sql.Rows) ([]SourceInfo, error) {
	var result []SourceInfo
	for rows.Next() {
		var s SourceInfo
		if err := rows.Scan(&s.SourceID, &s.Title, &s.Summary, &s.DomainID, &s.MarkdownPath); err != nil {
			return nil, fmt.Errorf("retrieval store: scan source: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (s *Store) GetOutlinesBySourceIDs(sourceIDs []string) ([]OutlineInfo, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(sourceIDs))
	args := make([]interface{}, len(sourceIDs))
	for i, id := range sourceIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT outline_id, source_id, parent_id, level, title, summary, line_start, line_end
		 FROM source_outlines WHERE source_id IN (%s) ORDER BY line_start ASC`,
		strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get outlines: %w", err)
	}
	defer rows.Close()

	var result []OutlineInfo
	for rows.Next() {
		var o OutlineInfo
		if err := rows.Scan(&o.OutlineID, &o.SourceID, &o.ParentID, &o.Level, &o.Title, &o.Summary, &o.LineStart, &o.LineEnd); err != nil {
			return nil, fmt.Errorf("retrieval store: scan outline: %w", err)
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

func (s *Store) GetUnitsByOutlineIDs(outlineIDs []string) ([]UnitInfo, error) {
	if len(outlineIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(outlineIDs))
	args := make([]interface{}, len(outlineIDs))
	for i, id := range outlineIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT unit_id, source_id, outline_id, line_start, line_end
		 FROM knowledge_units WHERE outline_id IN (%s) AND status = 'completed'`,
		strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get units by outlines: %w", err)
	}
	defer rows.Close()

	var result []UnitInfo
	for rows.Next() {
		var u UnitInfo
		if err := rows.Scan(&u.UnitID, &u.SourceID, &u.OutlineID, &u.LineStart, &u.LineEnd); err != nil {
			return nil, fmt.Errorf("retrieval store: scan unit: %w", err)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Store) GetUnitByID(unitID string) (*UnitInfo, error) {
	u := &UnitInfo{}
	err := s.db.QueryRow(
		`SELECT unit_id, source_id, outline_id, line_start, line_end
		 FROM knowledge_units WHERE unit_id = ?`, unitID).Scan(
		&u.UnitID, &u.SourceID, &u.OutlineID, &u.LineStart, &u.LineEnd)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get unit: %w", err)
	}
	return u, nil
}

func (s *Store) GetSourceMarkdownPath(sourceID string) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT markdown_path FROM sources WHERE source_id = ?`, sourceID).Scan(&path)
	if err != nil {
		return "", fmt.Errorf("retrieval store: get markdown path: %w", err)
	}
	return path, nil
}

func (s *Store) GetFirstPointByUnitID(unitID string) (string, error) {
	var pointID string
	err := s.db.QueryRow(
		`SELECT point_id FROM knowledge_points WHERE unit_id = ? ORDER BY created_at ASC LIMIT 1`,
		unitID).Scan(&pointID)
	if err != nil {
		return "", fmt.Errorf("retrieval store: get first point: %w", err)
	}
	return pointID, nil
}

func (s *Store) GetPointUnitID(pointID string) (string, error) {
	var unitID string
	err := s.db.QueryRow(
		`SELECT unit_id FROM knowledge_points WHERE point_id = ?`, pointID).Scan(&unitID)
	if err != nil {
		return "", fmt.Errorf("retrieval store: get point unit id: %w", err)
	}
	return unitID, nil
}

func (s *Store) GetPointsByUnitIDs(unitIDs []string) ([]struct {
	PointID string
	UnitID  string
}, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(unitIDs))
	args := make([]interface{}, len(unitIDs))
	for i, id := range unitIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT point_id, unit_id FROM knowledge_points WHERE unit_id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get points by units: %w", err)
	}
	defer rows.Close()

	var result []struct {
		PointID string
		UnitID  string
	}
	for rows.Next() {
		var p struct {
			PointID string
			UnitID  string
		}
		if err := rows.Scan(&p.PointID, &p.UnitID); err != nil {
			return nil, fmt.Errorf("retrieval store: scan point: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

type KPNNeighbor struct {
	NeighborPointID string
	UnitID          string
}

// GetKPNNeighbors returns neighbor point_ids following direction rules,
// excluding contradicts relations (those are handled separately by GetKPNConflicts).
func (s *Store) GetKPNNeighbors(seedPointIDs []string) ([]KPNNeighbor, error) {
	if len(seedPointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(seedPointIDs)

	query := fmt.Sprintf(`
		SELECT DISTINCT kp.point_id, kp.unit_id FROM knowledge_points kp
		WHERE kp.point_id IN (
			SELECT target_point_id FROM knowledge_point_relations
			WHERE source_point_id IN (%s) AND relation_type != 'contradicts'
			UNION
			SELECT source_point_id FROM knowledge_point_relations
			WHERE target_point_id IN (%s) AND direction = 'bidirectional' AND relation_type != 'contradicts'
		)`, ph, ph)

	allArgs := append(args, args...)
	return scanKPNNeighbors(s.db, query, allArgs)
}

// GetKPNConflicts returns neighbor point_ids that have a contradicts relation with any seed point.
func (s *Store) GetKPNConflicts(seedPointIDs []string) ([]KPNNeighbor, error) {
	if len(seedPointIDs) == 0 {
		return nil, nil
	}
	ph, args := buildPlaceholders(seedPointIDs)

	query := fmt.Sprintf(`
		SELECT DISTINCT kp.point_id, kp.unit_id FROM knowledge_points kp
		WHERE kp.point_id IN (
			SELECT target_point_id FROM knowledge_point_relations
			WHERE source_point_id IN (%s) AND relation_type = 'contradicts'
			UNION
			SELECT source_point_id FROM knowledge_point_relations
			WHERE target_point_id IN (%s) AND relation_type = 'contradicts'
		)`, ph, ph)

	allArgs := append(args, args...)
	return scanKPNNeighbors(s.db, query, allArgs)
}

func (s *Store) GetSourceTitle(sourceID string) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM sources WHERE source_id = ?`, sourceID).Scan(&title)
	if err != nil {
		return "", fmt.Errorf("retrieval store: get source title: %w", err)
	}
	return title, nil
}

func buildPlaceholders(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func scanKPNNeighbors(db *sql.DB, query string, args []interface{}) ([]KPNNeighbor, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: kpn query: %w", err)
	}
	defer rows.Close()

	var result []KPNNeighbor
	for rows.Next() {
		var r KPNNeighbor
		if err := rows.Scan(&r.NeighborPointID, &r.UnitID); err != nil {
			return nil, fmt.Errorf("retrieval store: scan neighbor: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetChildOutlineIDs returns the given outline IDs plus all their descendant outline IDs.
func (s *Store) GetChildOutlineIDs(outlineIDs []string, sourceID string) ([]string, error) {
	if len(outlineIDs) == 0 {
		return nil, nil
	}
	// Get all outlines for the source and build tree in memory
	rows, err := s.db.Query(
		`SELECT outline_id, parent_id FROM source_outlines WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("retrieval store: get child outlines: %w", err)
	}
	defer rows.Close()

	children := make(map[string][]string)
	for rows.Next() {
		var id string
		var parentID sql.NullString
		if err := rows.Scan(&id, &parentID); err != nil {
			return nil, fmt.Errorf("retrieval store: scan outline id: %w", err)
		}
		if parentID.Valid {
			children[parentID.String] = append(children[parentID.String], id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var collect func(id string)
	collect = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, child := range children[id] {
			collect(child)
		}
	}
	for _, id := range outlineIDs {
		collect(id)
	}

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result, nil
}
