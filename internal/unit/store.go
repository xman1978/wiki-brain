package unit

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Lifecycle states for KnowledgeUnit / KnowledgePoint (docs/impl/v1/lifecycle.md).
// current is the only state that participates in retrieval; superseded and
// deprecated are retained for traceability only. KP lifecycle always follows
// its owning KU — never set independently outside SetUnitLifecycle.
const (
	LifecycleCurrent    = "current"
	LifecycleSuperseded = "superseded"
	LifecycleDeprecated = "deprecated"
)

type KnowledgeUnit struct {
	UnitID             string
	SourceID           string
	OutlineID          sql.NullString
	ConceptID          sql.NullString
	Center             string
	LineStart          int
	LineEnd            int
	Status             string
	ErrorMsg           sql.NullString
	PromptVersion      string
	Lifecycle          string
	LifecycleChangedAt sql.NullTime
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type KnowledgePoint struct {
	PointID            string
	UnitID             string
	SourceID           string
	Content            string
	PointType          string
	Lifecycle          string
	LifecycleChangedAt sql.NullTime
	CreatedAt          time.Time
}

type KnowledgePointRelation struct {
	RelationID    string
	SourcePointID string
	TargetPointID string
	RelationType  string
	Direction     string
	PromptVersion string
	CreatedAt     time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) InsertUnit(ku *KnowledgeUnit) error {
	if ku.UnitID == "" {
		ku.UnitID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ku.UnitID, ku.SourceID, ku.OutlineID, ku.ConceptID, ku.Center,
		ku.LineStart, ku.LineEnd, ku.Status, ku.ErrorMsg, ku.PromptVersion)
	if err != nil {
		return fmt.Errorf("unit store: insert unit: %w", err)
	}
	return nil
}

func (s *Store) InsertPoint(kp *KnowledgePoint) error {
	if kp.PointID == "" {
		kp.PointID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES (?, ?, ?, ?, ?)`,
		kp.PointID, kp.UnitID, kp.SourceID, kp.Content, kp.PointType)
	if err != nil {
		return fmt.Errorf("unit store: insert point: %w", err)
	}
	return nil
}

func (s *Store) InsertRelation(r *KnowledgePointRelation) error {
	if r.RelationID == "" {
		r.RelationID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.RelationID, r.SourcePointID, r.TargetPointID, r.RelationType, r.Direction, r.PromptVersion)
	if err != nil {
		return fmt.Errorf("unit store: insert relation: %w", err)
	}
	return nil
}

func (s *Store) UpdateUnitStatus(unitID, status string, errorMsg *string) error {
	var errVal sql.NullString
	if errorMsg != nil {
		errVal = sql.NullString{String: *errorMsg, Valid: true}
	}
	_, err := s.db.Exec(`UPDATE knowledge_units SET status = ?, error_msg = ?, updated_at = CURRENT_TIMESTAMP WHERE unit_id = ?`,
		status, errVal, unitID)
	if err != nil {
		return fmt.Errorf("unit store: update unit status: %w", err)
	}
	return nil
}

func (s *Store) UpdateUnitConceptID(unitID string, conceptID *string) error {
	var val sql.NullString
	if conceptID != nil && *conceptID != "" {
		val = sql.NullString{String: *conceptID, Valid: true}
	}
	_, err := s.db.Exec(`UPDATE knowledge_units SET concept_id = ?, updated_at = CURRENT_TIMESTAMP WHERE unit_id = ?`,
		val, unitID)
	if err != nil {
		return fmt.Errorf("unit store: update concept id: %w", err)
	}
	return nil
}

func (s *Store) GetUnitsBySourceID(sourceID string) ([]KnowledgeUnit, error) {
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? ORDER BY line_start ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by source: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.ConceptID, &ku.Center,
			&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
			&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan unit: %w", err)
		}
		units = append(units, ku)
	}
	return units, rows.Err()
}

// GetUnitsBySourceIDFiltered is like GetUnitsBySourceID but restricts to a
// single lifecycle state when lifecycle is non-empty (GET /sources/:id/units?lifecycle=...).
func (s *Store) GetUnitsBySourceIDFiltered(sourceID, lifecycle string) ([]KnowledgeUnit, error) {
	if lifecycle == "" {
		return s.GetUnitsBySourceID(sourceID)
	}
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? AND lifecycle = ? ORDER BY line_start ASC`, sourceID, lifecycle)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by source filtered: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.ConceptID, &ku.Center,
			&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
			&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan unit: %w", err)
		}
		units = append(units, ku)
	}
	return units, rows.Err()
}

// GetUnitsByIDs fetches multiple units by id, used by SetUnitLifecycle to read
// current field values (source_id, line range) before cascading and reindexing.
func (s *Store) GetUnitsByIDs(unitIDs []string) ([]KnowledgeUnit, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by ids: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.ConceptID, &ku.Center,
			&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
			&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan unit: %w", err)
		}
		units = append(units, ku)
	}
	return units, rows.Err()
}

// UpdateUnitsLifecycle bulk-updates the lifecycle state of the given units.
// Callers must go through Service.SetUnitLifecycle, which also cascades to
// knowledge_points and reindexes Bleve — never call this directly.
func (s *Store) UpdateUnitsLifecycle(unitIDs []string, lifecycle string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, 0, len(unitIDs)+1)
	args = append(args, lifecycle)
	for _, id := range unitIDs {
		args = append(args, id)
	}
	_, err := s.db.Exec(fmt.Sprintf(`UPDATE knowledge_units SET lifecycle = ?, lifecycle_changed_at = CURRENT_TIMESTAMP WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("unit store: update units lifecycle: %w", err)
	}
	return nil
}

// UpdatePointsLifecycleByUnitIDs cascades a lifecycle change from KU to its KPs.
// See UpdateUnitsLifecycle — internal to Service.SetUnitLifecycle only.
func (s *Store) UpdatePointsLifecycleByUnitIDs(unitIDs []string, lifecycle string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, 0, len(unitIDs)+1)
	args = append(args, lifecycle)
	for _, id := range unitIDs {
		args = append(args, id)
	}
	_, err := s.db.Exec(fmt.Sprintf(`UPDATE knowledge_points SET lifecycle = ?, lifecycle_changed_at = CURRENT_TIMESTAMP WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("unit store: update points lifecycle: %w", err)
	}
	return nil
}

// GetPointsByUnitIDs fetches all points for a set of units (bulk form of
// GetPointsByUnitID), used by SetUnitLifecycle to reindex cascaded KPs.
func (s *Store) GetPointsByUnitIDs(unitIDs []string) ([]KnowledgePoint, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at
		FROM knowledge_points WHERE unit_id IN (%s) ORDER BY created_at ASC`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get points by unit ids: %w", err)
	}
	defer rows.Close()

	var points []KnowledgePoint
	for rows.Next() {
		var kp KnowledgePoint
		if err := rows.Scan(&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
			&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan point: %w", err)
		}
		points = append(points, kp)
	}
	return points, rows.Err()
}

func (s *Store) GetCompletedUnitsBySourceID(sourceID string) ([]KnowledgeUnit, error) {
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? AND status = 'completed' ORDER BY line_start ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get completed units: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.ConceptID, &ku.Center,
			&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
			&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan unit: %w", err)
		}
		units = append(units, ku)
	}
	return units, rows.Err()
}

func (s *Store) GetUnitByID(unitID string) (*KnowledgeUnit, error) {
	ku := &KnowledgeUnit{}
	err := s.db.QueryRow(`SELECT unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE unit_id = ?`, unitID).Scan(
		&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.ConceptID, &ku.Center,
		&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
		&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("unit store: get unit by id: %w", err)
	}
	return ku, nil
}

func (s *Store) GetPointByID(pointID string) (*KnowledgePoint, error) {
	kp := &KnowledgePoint{}
	err := s.db.QueryRow(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at
		FROM knowledge_points WHERE point_id = ?`, pointID).Scan(
		&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
		&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("unit store: get point by id: %w", err)
	}
	return kp, nil
}

func (s *Store) GetPointsByUnitID(unitID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at
		FROM knowledge_points WHERE unit_id = ? ORDER BY created_at ASC`, unitID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get points by unit: %w", err)
	}
	defer rows.Close()

	var points []KnowledgePoint
	for rows.Next() {
		var kp KnowledgePoint
		if err := rows.Scan(&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
			&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan point: %w", err)
		}
		points = append(points, kp)
	}
	return points, rows.Err()
}

func (s *Store) GetPointsBySourceID(sourceID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		INNER JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.source_id = ? AND ku.status = 'completed'
		ORDER BY kp.created_at ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get points by source: %w", err)
	}
	defer rows.Close()

	var points []KnowledgePoint
	for rows.Next() {
		var kp KnowledgePoint
		if err := rows.Scan(&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
			&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan point: %w", err)
		}
		points = append(points, kp)
	}
	return points, rows.Err()
}

func (s *Store) GetRelationsByPointID(pointID string) ([]KnowledgePointRelation, error) {
	rows, err := s.db.Query(`SELECT relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, created_at
		FROM knowledge_point_relations
		WHERE source_point_id = ? OR target_point_id = ?
		ORDER BY created_at ASC`, pointID, pointID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get relations by point: %w", err)
	}
	defer rows.Close()

	var relations []KnowledgePointRelation
	for rows.Next() {
		var r KnowledgePointRelation
		if err := rows.Scan(&r.RelationID, &r.SourcePointID, &r.TargetPointID, &r.RelationType, &r.Direction, &r.PromptVersion, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan relation: %w", err)
		}
		relations = append(relations, r)
	}
	return relations, rows.Err()
}

func (s *Store) GetConceptsByDomainID(domainID string) ([]Concept, error) {
	var rows *sql.Rows
	var err error
	if domainID != "" {
		rows, err = s.db.Query(`SELECT concept_id, domain_id, name, description FROM concepts WHERE domain_id = ? ORDER BY name`, domainID)
	} else {
		rows, err = s.db.Query(`SELECT concept_id, domain_id, name, description FROM concepts ORDER BY name`)
	}
	if err != nil {
		return nil, fmt.Errorf("unit store: get concepts: %w", err)
	}
	defer rows.Close()

	var concepts []Concept
	for rows.Next() {
		var c Concept
		var desc sql.NullString
		if err := rows.Scan(&c.ConceptID, &c.DomainID, &c.Name, &desc); err != nil {
			return nil, fmt.Errorf("unit store: scan concept: %w", err)
		}
		if desc.Valid {
			c.Description = desc.String
		}
		concepts = append(concepts, c)
	}
	return concepts, rows.Err()
}

type Concept struct {
	ConceptID   string
	DomainID    string
	Name        string
	Description string
}
