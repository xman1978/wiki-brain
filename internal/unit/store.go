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

// Relation scope (docs/impl/v1/kpn.md 数据结构).
const (
	RelationScopeIntra = "intra"
	RelationScopeCross = "cross"
)

type KnowledgePointRelation struct {
	RelationID    string
	SourcePointID string
	TargetPointID string
	RelationType  string
	Direction     string
	PromptVersion string
	Scope         string
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

// InsertRelation is INSERT OR IGNORE against idx_kp_relations_uniq
// (source_point_id, target_point_id, relation_type) — both the existing
// intra-Source path and the new cross-Source path (docs/impl/v1/kpn.md 步骤
// 4) share this single write path, so any duplicate (same batch re-run,
// re-triggered cross match, or an LLM re-proposing an existing pair) is
// silently a no-op rather than a constraint-violation error. Returns whether
// a row was actually inserted, so callers can report accurate counts.
func (s *Store) InsertRelation(r *KnowledgePointRelation) (bool, error) {
	if r.RelationID == "" {
		r.RelationID = uuid.New().String()
	}
	if r.Scope == "" {
		r.Scope = RelationScopeIntra
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO knowledge_point_relations
		(relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.RelationID, r.SourcePointID, r.TargetPointID, r.RelationType, r.Direction, r.PromptVersion, r.Scope)
	if err != nil {
		return false, fmt.Errorf("unit store: insert relation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unit store: insert relation rows affected: %w", err)
	}
	return n > 0, nil
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

// SnapshotLifecycleBeforeDelete copies each unit's current lifecycle value
// into lifecycle_before_delete, so a later restore can reverse a source
// soft-delete's deprecation precisely instead of blindly resetting every
// unit to current. Internal to Service.SnapshotAndDeprecate only.
func (s *Store) SnapshotLifecycleBeforeDelete(unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	_, err := s.db.Exec(fmt.Sprintf(`UPDATE knowledge_units SET lifecycle_before_delete = lifecycle WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("unit store: snapshot lifecycle before delete: %w", err)
	}
	return nil
}

// GroupUnitIDsByLifecycleBeforeDelete groups the given units by their
// snapshotted lifecycle_before_delete value. Units with no snapshot (NULL —
// never soft-deleted, or already restored) are omitted from the result.
// Internal to Service.RestoreLifecycle only.
func (s *Store) GroupUnitIDsByLifecycleBeforeDelete(unitIDs []string) (map[string][]string, error) {
	if len(unitIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT unit_id, lifecycle_before_delete FROM knowledge_units WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: group units by lifecycle_before_delete: %w", err)
	}
	defer rows.Close()

	groups := make(map[string][]string)
	for rows.Next() {
		var unitID string
		var prior sql.NullString
		if err := rows.Scan(&unitID, &prior); err != nil {
			return nil, fmt.Errorf("unit store: scan lifecycle_before_delete: %w", err)
		}
		if !prior.Valid {
			continue
		}
		groups[prior.String] = append(groups[prior.String], unitID)
	}
	return groups, rows.Err()
}

// ClearLifecycleBeforeDelete resets the snapshot column back to NULL after a
// successful restore. Internal to Service.RestoreLifecycle only.
func (s *Store) ClearLifecycleBeforeDelete(unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	_, err := s.db.Exec(fmt.Sprintf(`UPDATE knowledge_units SET lifecycle_before_delete = NULL WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("unit store: clear lifecycle_before_delete: %w", err)
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

// GetPointsBySourceID returns lifecycle=current KPs (with a lifecycle=current,
// status=completed KU) for sourceID — both the intra-Source KPN pass (right
// after extraction, where this filter is a no-op) and the cross-Source "新
// KP 集合" (docs/impl/v1/kpn.md 步骤 2) use this; the filter matters for the
// latter when re-triggered on a Source that has since been partially
// superseded by a reupload.
func (s *Store) GetPointsBySourceID(sourceID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		INNER JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.source_id = ? AND ku.status = 'completed' AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
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

// GetCrossSourcePointsByConceptID returns lifecycle=current KPs (with a
// current KU) sharing conceptID, excluding excludeSourceID — priority-1 "对端
// KP 集合" (docs/impl/v1/kpn.md 步骤 2).
func (s *Store) GetCrossSourcePointsByConceptID(conceptID, excludeSourceID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.concept_id = ? AND kp.source_id != ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		ORDER BY kp.created_at ASC`, conceptID, excludeSourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get cross source points by concept: %w", err)
	}
	defer rows.Close()
	return scanKnowledgePoints(rows)
}

// GetCrossSourcePointsByDomainID returns lifecycle=current KPs (with a
// current KU) whose Source is under domainID, excluding excludeSourceID —
// priority-2 fallback "对端 KP 集合" when the new KU has no concept_id
// (docs/impl/v1/kpn.md 步骤 2).
func (s *Store) GetCrossSourcePointsByDomainID(domainID, excludeSourceID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN sources src ON ku.source_id = src.source_id
		WHERE src.domain_id = ? AND kp.source_id != ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		ORDER BY kp.created_at ASC`, domainID, excludeSourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get cross source points by domain: %w", err)
	}
	defer rows.Close()
	return scanKnowledgePoints(rows)
}

func scanKnowledgePoints(rows *sql.Rows) ([]KnowledgePoint, error) {
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

// ConfidentCountByPointIDs reads question_kp_cooccurrence.confident_count
// (a Trace-owned table, read-only here) for the given point_ids, used to
// prioritize which cross-Source candidates survive truncation when a batch
// is over the size cap (docs/impl/v1/kpn.md 步骤 2, "优先与被验证过的知识建立关系").
func (s *Store) ConfidentCountByPointIDs(pointIDs []string) (map[string]int, error) {
	result := make(map[string]int, len(pointIDs))
	if len(pointIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pointIDs)), ",")
	args := make([]any, len(pointIDs))
	for i, id := range pointIDs {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT point_id, confident_count FROM question_kp_cooccurrence WHERE point_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: confident count by points: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pointID string
		var count int
		if err := rows.Scan(&pointID, &count); err != nil {
			return nil, fmt.Errorf("unit store: scan confident count: %w", err)
		}
		if count > result[pointID] {
			result[pointID] = count
		}
	}
	return result, rows.Err()
}

func (s *Store) GetRelationsByPointID(pointID, scope string) ([]KnowledgePointRelation, error) {
	query := `SELECT relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope, created_at
		FROM knowledge_point_relations
		WHERE (source_point_id = ? OR target_point_id = ?)`
	args := []any{pointID, pointID}
	if scope != "" {
		query += ` AND scope = ?`
		args = append(args, scope)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get relations by point: %w", err)
	}
	defer rows.Close()

	var relations []KnowledgePointRelation
	for rows.Next() {
		var r KnowledgePointRelation
		if err := rows.Scan(&r.RelationID, &r.SourcePointID, &r.TargetPointID, &r.RelationType, &r.Direction, &r.PromptVersion, &r.Scope, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan relation: %w", err)
		}
		relations = append(relations, r)
	}
	return relations, rows.Err()
}

// GetConceptsByDomainID lists candidate concepts for unit_concept_match,
// excluding merged_into non-NULL rows — a merged concept is no longer a
// valid match target (docs/impl/v1/concept-evolution.md 步骤 4).
func (s *Store) GetConceptsByDomainID(domainID string) ([]Concept, error) {
	var rows *sql.Rows
	var err error
	if domainID != "" {
		rows, err = s.db.Query(`SELECT concept_id, domain_id, name, description FROM concepts WHERE domain_id = ? AND merged_into IS NULL ORDER BY name`, domainID)
	} else {
		rows, err = s.db.Query(`SELECT concept_id, domain_id, name, description FROM concepts WHERE merged_into IS NULL ORDER BY name`)
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
