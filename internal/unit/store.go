package unit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/rerank"
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
	EntryID          sql.NullString
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
	ManuallyEdited     bool
	EditedAt           sql.NullTime
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

// PublishGeneration replaces a source's current generation in one SQLite
// transaction. The returned rows describe every document that must be
// rewritten in Bleve after commit: superseded units and their points, plus
// the newly inserted current units and points.
func (s *Store) PublishGeneration(
	sourceID string,
	pool []unitCandidate,
	semantics map[string]rerank.Semantics,
) (superseded []KnowledgeUnit, inserted []KnowledgeUnit, points []KnowledgePoint, err error) {
	if err := validatePublicationSemantics(pool, semantics); err != nil {
		return nil, nil, nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unit store: publish generation: begin: %w", err)
	}
	defer tx.Rollback()

	current, err := getCurrentUnitsBySourceIDTx(tx, sourceID)
	if err != nil {
		return nil, nil, nil, err
	}
	currentIDs := make([]string, len(current))
	for i := range current {
		currentIDs[i] = current[i].UnitID
	}
	if len(currentIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(currentIDs)), ",")
		args := make([]any, 0, len(currentIDs)+1)
		args = append(args, LifecycleSuperseded)
		for _, id := range currentIDs {
			args = append(args, id)
		}
		if _, err := tx.Exec(fmt.Sprintf(`UPDATE knowledge_units
			SET lifecycle = ?, lifecycle_changed_at = CURRENT_TIMESTAMP
			WHERE unit_id IN (%s)`, placeholders), args...); err != nil {
			return nil, nil, nil, fmt.Errorf("unit store: publish generation: supersede units: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`UPDATE knowledge_points
			SET lifecycle = ?, lifecycle_changed_at = CURRENT_TIMESTAMP
			WHERE unit_id IN (%s)`, placeholders), args...); err != nil {
			return nil, nil, nil, fmt.Errorf("unit store: publish generation: supersede points: %w", err)
		}
		superseded, err = getUnitsByIDsTx(tx, currentIDs)
		if err != nil {
			return nil, nil, nil, err
		}
		points, err = getPointsByUnitIDsTx(tx, currentIDs)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	inserted = make([]KnowledgeUnit, 0, len(pool))
	for _, candidate := range pool {
		// A unit_id absent from semantics is one that extractRerankSemantics
		// gave up on even after every fallback tier (see rerank_semantics.go)
		// — the whole unit is discarded rather than published without a
		// usable rerank signal: it would never be selectable by rerank
		// anyway (see the retrieval integrity check), so keeping it around
		// with no semantics row would only be dead weight. Every other
		// candidate in the same pool still publishes normally.
		semantic, ok := semantics[candidate.id]
		if !ok {
			continue
		}

		promptVersion := candidate.promptVersion
		if promptVersion == "" {
			promptVersion = promptVersionSplitExtract
		}
		unit := KnowledgeUnit{
			UnitID:        candidate.id,
			SourceID:      sourceID,
			OutlineID:     candidate.seg.OutlineID,
			Center:        candidate.llm.Center,
			LineStart:     candidate.lineStart,
			LineEnd:       candidate.lineEnd,
			Status:        "completed",
			PromptVersion: promptVersion,
			Lifecycle:     LifecycleCurrent,
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_units
			(unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			unit.UnitID, unit.SourceID, unit.OutlineID, unit.EntryID, unit.Center,
			unit.LineStart, unit.LineEnd, unit.Status, unit.ErrorMsg, unit.PromptVersion, unit.Lifecycle); err != nil {
			return nil, nil, nil, fmt.Errorf("unit store: publish generation: insert unit %s: %w", unit.UnitID, err)
		}

		for _, candidatePoint := range candidate.points {
			point := KnowledgePoint{
				PointID:   uuid.New().String(),
				UnitID:    unit.UnitID,
				SourceID:  sourceID,
				Content:   candidatePoint.Content,
				PointType: candidatePoint.Type,
				Lifecycle: LifecycleCurrent,
			}
			if _, err := tx.Exec(`INSERT INTO knowledge_points
				(point_id, unit_id, source_id, content, point_type, lifecycle)
				VALUES (?, ?, ?, ?, ?, ?)`,
				point.PointID, point.UnitID, point.SourceID, point.Content, point.PointType, point.Lifecycle); err != nil {
				return nil, nil, nil, fmt.Errorf("unit store: publish generation: insert point for unit %s: %w", unit.UnitID, err)
			}
			points = append(points, point)
		}

		if _, err := tx.Exec(`INSERT INTO unit_rerank_semantics
			(unit_id, source_theme, content_theme, intent, object, scope, prompt_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			unit.UnitID, semantic.SourceTheme, semantic.ContentTheme, semantic.Intent,
			semantic.Object, semantic.Scope, semantic.PromptVersion); err != nil {
			return nil, nil, nil, fmt.Errorf("unit store: publish generation: insert semantics for unit %s: %w", unit.UnitID, err)
		}
		inserted = append(inserted, unit)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, nil, fmt.Errorf("unit store: publish generation: commit: %w", err)
	}
	return superseded, inserted, points, nil
}

// validatePublicationSemantics validates whatever semantics were actually
// produced — a candidate with no entry in semantics at all is tolerated here
// (it's simply discarded later, see the loop above) since
// extractRerankSemantics already gave up on it after every fallback tier;
// what it does return an entry for must still be well-formed.
func validatePublicationSemantics(pool []unitCandidate, semantics map[string]rerank.Semantics) error {
	candidateIDs := make(map[string]bool, len(pool))
	for _, candidate := range pool {
		if candidate.id == "" {
			return fmt.Errorf("unit store: publish generation: candidate has empty unit_id")
		}
		if candidateIDs[candidate.id] {
			return fmt.Errorf("unit store: publish generation: duplicate candidate unit_id %s", candidate.id)
		}
		candidateIDs[candidate.id] = true

		semantic, ok := semantics[candidate.id]
		if !ok {
			continue
		}
		if err := validateSemantic(candidate.id, semantic); err != nil {
			return fmt.Errorf("unit store: publish generation: %w", err)
		}
	}
	for unitID := range semantics {
		if !candidateIDs[unitID] {
			return fmt.Errorf("unit store: publish generation: extra semantics for unit_id %s", unitID)
		}
	}
	return nil
}

// validateSemantic validates one unit's semantics (the same rules
// validatePublicationSemantics applies per-candidate). Shared by
// validatePublicationSemantics (a whole generation) and InsertStandaloneUnit
// (a single manually-fixed unit) so both insert paths enforce identical row
// well-formedness.
func validateSemantic(unitID string, semantic rerank.Semantics) error {
	if semantic.UnitID != unitID {
		return fmt.Errorf("semantic unit_id %s does not match %s", semantic.UnitID, unitID)
	}
	if semantic.PromptVersion != rerank.ExtractPromptVersion {
		return fmt.Errorf("semantic prompt_version for unit %s = %q, want %q",
			unitID, semantic.PromptVersion, rerank.ExtractPromptVersion)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "source_theme", value: semantic.SourceTheme},
		{name: "content_theme", value: semantic.ContentTheme},
		{name: "intent", value: semantic.Intent},
		{name: "object", value: semantic.Object},
		{name: "scope", value: semantic.Scope},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("semantic %s is empty for unit %s", field.name, unitID)
		}
	}
	return nil
}

// InsertStandaloneUnit inserts one new current knowledge unit together with
// its points and rerank semantics in a single transaction. Unlike
// PublishGeneration, it is purely additive — it never supersedes any other
// unit for the source — so it's the insert path for manually recovering one
// coverage gap (see Service.FixCoverageGap) without re-running the source's
// whole extraction generation.
func (s *Store) InsertStandaloneUnit(ku *KnowledgeUnit, points []KnowledgePoint, sem rerank.Semantics) error {
	if ku.UnitID == "" {
		return fmt.Errorf("unit store: insert standalone unit: empty unit_id")
	}
	if err := validateSemantic(ku.UnitID, sem); err != nil {
		return fmt.Errorf("unit store: insert standalone unit: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("unit store: insert standalone unit: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO knowledge_units
		(unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ku.UnitID, ku.SourceID, ku.OutlineID, ku.EntryID, ku.Center,
		ku.LineStart, ku.LineEnd, ku.Status, ku.ErrorMsg, ku.PromptVersion, LifecycleCurrent); err != nil {
		return fmt.Errorf("unit store: insert standalone unit: insert unit: %w", err)
	}

	for i := range points {
		if points[i].PointID == "" {
			points[i].PointID = uuid.New().String()
		}
		points[i].UnitID = ku.UnitID
		if _, err := tx.Exec(`INSERT INTO knowledge_points
			(point_id, unit_id, source_id, content, point_type, lifecycle)
			VALUES (?, ?, ?, ?, ?, ?)`,
			points[i].PointID, points[i].UnitID, points[i].SourceID, points[i].Content, points[i].PointType, LifecycleCurrent); err != nil {
			return fmt.Errorf("unit store: insert standalone unit: insert point: %w", err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ku.UnitID, sem.SourceTheme, sem.ContentTheme, sem.Intent, sem.Object, sem.Scope, sem.PromptVersion); err != nil {
		return fmt.Errorf("unit store: insert standalone unit: insert semantics: %w", err)
	}

	return tx.Commit()
}

func getCurrentUnitsBySourceIDTx(tx *sql.Tx, sourceID string) ([]KnowledgeUnit, error) {
	rows, err := tx.Query(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? AND lifecycle = ? ORDER BY line_start ASC`, sourceID, LifecycleCurrent)
	if err != nil {
		return nil, fmt.Errorf("unit store: publish generation: read current units: %w", err)
	}
	return scanKnowledgeUnitsForPublish(rows)
}

func getUnitsByIDsTx(tx *sql.Tx, unitIDs []string) ([]KnowledgeUnit, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	rows, err := tx.Query(fmt.Sprintf(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE unit_id IN (%s) ORDER BY line_start ASC`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: publish generation: read superseded units: %w", err)
	}
	return scanKnowledgeUnitsForPublish(rows)
}

func scanKnowledgeUnitsForPublish(rows *sql.Rows) ([]KnowledgeUnit, error) {
	defer rows.Close()
	var units []KnowledgeUnit
	for rows.Next() {
		var unit KnowledgeUnit
		if err := rows.Scan(&unit.UnitID, &unit.SourceID, &unit.OutlineID, &unit.EntryID, &unit.Center,
			&unit.LineStart, &unit.LineEnd, &unit.Status, &unit.ErrorMsg, &unit.PromptVersion,
			&unit.Lifecycle, &unit.LifecycleChangedAt, &unit.CreatedAt, &unit.UpdatedAt); err != nil {
			return nil, fmt.Errorf("unit store: publish generation: scan unit: %w", err)
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unit store: publish generation: iterate units: %w", err)
	}
	return units, nil
}

func getPointsByUnitIDsTx(tx *sql.Tx, unitIDs []string) ([]KnowledgePoint, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		args[i] = id
	}
	rows, err := tx.Query(fmt.Sprintf(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at
		FROM knowledge_points WHERE unit_id IN (%s) ORDER BY created_at ASC`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: publish generation: read superseded points: %w", err)
	}
	defer rows.Close()

	var points []KnowledgePoint
	for rows.Next() {
		var point KnowledgePoint
		if err := rows.Scan(&point.PointID, &point.UnitID, &point.SourceID, &point.Content, &point.PointType,
			&point.Lifecycle, &point.LifecycleChangedAt, &point.CreatedAt); err != nil {
			return nil, fmt.Errorf("unit store: publish generation: scan point: %w", err)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unit store: publish generation: iterate points: %w", err)
	}
	return points, nil
}

func (s *Store) InsertUnit(ku *KnowledgeUnit) error {
	if ku.UnitID == "" {
		ku.UnitID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ku.UnitID, ku.SourceID, ku.OutlineID, ku.EntryID, ku.Center,
		ku.LineStart, ku.LineEnd, ku.Status, ku.ErrorMsg, ku.PromptVersion)
	if err != nil {
		return fmt.Errorf("unit store: insert unit: %w", err)
	}
	return nil
}

// UpdateUnitCenterAndBounds rewrites a unit's topic and line range in one
// statement — used by extraction-time duplicate consolidation (see
// dedup.go) to turn the surviving unit of a merge into the union of both
// original units' bounds under the LLM's merged center.
func (s *Store) UpdateUnitCenterAndBounds(unitID, center string, lineStart, lineEnd int) error {
	_, err := s.db.Exec(`UPDATE knowledge_units SET center = ?, line_start = ?, line_end = ?, updated_at = CURRENT_TIMESTAMP WHERE unit_id = ?`,
		center, lineStart, lineEnd, unitID)
	if err != nil {
		return fmt.Errorf("unit store: update unit center and bounds: %w", err)
	}
	return nil
}

// DeletePointsByUnitID clears a unit's existing points so dedup can replace
// them with the LLM's deduplicated set (see dedup.go).
func (s *Store) DeletePointsByUnitID(unitID string) error {
	_, err := s.db.Exec(`DELETE FROM knowledge_points WHERE unit_id = ?`, unitID)
	if err != nil {
		return fmt.Errorf("unit store: delete points by unit id: %w", err)
	}
	return nil
}

// DeleteUnitAndPoints hard-deletes a knowledge unit and its points. This is
// NOT the lifecycle soft-delete used elsewhere in this package (see
// Service.SetUnitLifecycle) — it's only safe because extraction-time
// duplicate consolidation (dedup.go) runs before KPN generation, concept
// matching, or anything else downstream could have referenced either unit's
// rows yet.
func (s *Store) DeleteUnitAndPoints(unitID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("unit store: delete unit: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM knowledge_points WHERE unit_id = ?`, unitID); err != nil {
		return fmt.Errorf("unit store: delete unit: delete points: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_units WHERE unit_id = ?`, unitID); err != nil {
		return fmt.Errorf("unit store: delete unit: delete unit: %w", err)
	}
	return tx.Commit()
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

// InsertManualPoint inserts a human-added KP (docs/impl/v1/semantics-curation.md
// "KP 人工修正"), setting manually_edited=1/edited_at=now — the same
// protection UpsertManualRerankSemantics gives hand-edited rerank semantics.
// lifecycle defaults to current at the schema level.
func (s *Store) InsertManualPoint(kp *KnowledgePoint) error {
	if kp.PointID == "" {
		kp.PointID = uuid.New().String()
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_points
		(point_id, unit_id, source_id, content, point_type, manually_edited, edited_at)
		VALUES (?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)`,
		kp.PointID, kp.UnitID, kp.SourceID, kp.Content, kp.PointType)
	if err != nil {
		return fmt.Errorf("unit store: insert manual point: %w", err)
	}
	return nil
}

// UpdateManualPoint edits an existing KP's content/point_type and marks it
// manually_edited=1/edited_at=now, protecting it from future automated
// rewrites the same way UpsertManualRerankSemantics protects an edited
// unit_rerank_semantics row.
func (s *Store) UpdateManualPoint(pointID, content, pointType string) error {
	res, err := s.db.Exec(`UPDATE knowledge_points
		SET content = ?, point_type = ?, manually_edited = 1, edited_at = CURRENT_TIMESTAMP
		WHERE point_id = ?`,
		content, pointType, pointID)
	if err != nil {
		return fmt.Errorf("unit store: update manual point: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("unit store: update manual point: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("unit store: update manual point: point %s not found", pointID)
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

// UpdateUnitBounds widens a completed unit's line range to absorb a
// neighboring coverage gap (see gapfill.go). Only line_start/line_end
// change — center/points are untouched, since the gap's content wasn't
// judged to warrant its own summary.
func (s *Store) UpdateUnitBounds(unitID string, lineStart, lineEnd int) error {
	_, err := s.db.Exec(`UPDATE knowledge_units SET line_start = ?, line_end = ?, updated_at = CURRENT_TIMESTAMP WHERE unit_id = ?`,
		lineStart, lineEnd, unitID)
	if err != nil {
		return fmt.Errorf("unit store: update unit bounds: %w", err)
	}
	return nil
}

// MovePointToUnit reparents a knowledge point to another unit, keeping its
// point_id stable — traces, activation links, and KPN relations reference
// points by id, so an offline duplicate merge moves the losing unit's unique
// points instead of copying them (see ApplyOfflineMerge).
func (s *Store) MovePointToUnit(pointID, unitID string) error {
	_, err := s.db.Exec(`UPDATE knowledge_points SET unit_id = ? WHERE point_id = ?`,
		unitID, pointID)
	if err != nil {
		return fmt.Errorf("unit store: move point to unit: %w", err)
	}
	return nil
}

func (s *Store) UpdateUnitEntryID(unitID string, conceptID *string) error {
	var val sql.NullString
	if conceptID != nil && *conceptID != "" {
		val = sql.NullString{String: *conceptID, Valid: true}
	}
	_, err := s.db.Exec(`UPDATE knowledge_units SET entry_id = ?, updated_at = CURRENT_TIMESTAMP WHERE unit_id = ?`,
		val, unitID)
	if err != nil {
		return fmt.Errorf("unit store: update concept id: %w", err)
	}
	return nil
}

// ClearEntryIDBySourceID resets entry_id for all of a source's current
// KUs — used before a domain-switch re-match (MatchEntries) so a KU that no
// longer fits any concept in the new domain ends up unclassified instead of
// silently keeping a entry_id that belongs to its old domain (matchEntries
// only ever writes a match it found; it never clears one that came up empty).
func (s *Store) ClearEntryIDBySourceID(sourceID string) error {
	_, err := s.db.Exec(`UPDATE knowledge_units SET entry_id = NULL, updated_at = CURRENT_TIMESTAMP WHERE source_id = ? AND lifecycle = 'current'`, sourceID)
	if err != nil {
		return fmt.Errorf("unit store: clear concept id by source: %w", err)
	}
	return nil
}

func (s *Store) GetUnitsBySourceID(sourceID string) ([]KnowledgeUnit, error) {
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? ORDER BY line_start ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by source: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.EntryID, &ku.Center,
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
// lifecycle=all (or empty) returns every row for the source.
func (s *Store) GetUnitsBySourceIDFiltered(sourceID, lifecycle string) ([]KnowledgeUnit, error) {
	if lifecycle == "" || lifecycle == "all" {
		return s.GetUnitsBySourceID(sourceID)
	}
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? AND lifecycle = ? ORDER BY line_start ASC`, sourceID, lifecycle)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by source filtered: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.EntryID, &ku.Center,
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
	rows, err := s.db.Query(fmt.Sprintf(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get units by ids: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.EntryID, &ku.Center,
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
	rows, err := s.db.Query(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE source_id = ? AND status = 'completed' ORDER BY line_start ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get completed units: %w", err)
	}
	defer rows.Close()

	var units []KnowledgeUnit
	for rows.Next() {
		var ku KnowledgeUnit
		if err := rows.Scan(&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.EntryID, &ku.Center,
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
	err := s.db.QueryRow(`SELECT unit_id, source_id, outline_id, entry_id, center, line_start, line_end, status, error_msg, prompt_version, lifecycle, lifecycle_changed_at, created_at, updated_at
		FROM knowledge_units WHERE unit_id = ?`, unitID).Scan(
		&ku.UnitID, &ku.SourceID, &ku.OutlineID, &ku.EntryID, &ku.Center,
		&ku.LineStart, &ku.LineEnd, &ku.Status, &ku.ErrorMsg, &ku.PromptVersion,
		&ku.Lifecycle, &ku.LifecycleChangedAt, &ku.CreatedAt, &ku.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("unit store: get unit by id: %w", err)
	}
	return ku, nil
}

func (s *Store) GetPointByID(pointID string) (*KnowledgePoint, error) {
	kp := &KnowledgePoint{}
	var manuallyEdited int
	err := s.db.QueryRow(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at, manually_edited, edited_at
		FROM knowledge_points WHERE point_id = ?`, pointID).Scan(
		&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
		&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt, &manuallyEdited, &kp.EditedAt)
	if err != nil {
		return nil, fmt.Errorf("unit store: get point by id: %w", err)
	}
	kp.ManuallyEdited = manuallyEdited != 0
	return kp, nil
}

func (s *Store) GetPointsByUnitID(unitID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at, manually_edited, edited_at
		FROM knowledge_points WHERE unit_id = ? ORDER BY created_at ASC`, unitID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get points by unit: %w", err)
	}
	defer rows.Close()

	var points []KnowledgePoint
	for rows.Next() {
		var kp KnowledgePoint
		var manuallyEdited int
		if err := rows.Scan(&kp.PointID, &kp.UnitID, &kp.SourceID, &kp.Content, &kp.PointType,
			&kp.Lifecycle, &kp.LifecycleChangedAt, &kp.CreatedAt, &manuallyEdited, &kp.EditedAt); err != nil {
			return nil, fmt.Errorf("unit store: scan point: %w", err)
		}
		kp.ManuallyEdited = manuallyEdited != 0
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

// GetCrossSourcePointsByEntryID returns lifecycle=current KPs (with a
// current KU) sharing conceptID, excluding excludeSourceID — priority-1 "对端
// KP 集合" (docs/impl/v1/kpn.md 步骤 2).
func (s *Store) GetCrossSourcePointsByEntryID(conceptID, excludeSourceID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE ku.entry_id = ? AND kp.source_id != ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		ORDER BY kp.created_at ASC`, conceptID, excludeSourceID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get cross source points by concept: %w", err)
	}
	defer rows.Close()
	return scanKnowledgePoints(rows)
}

// GetCrossSourcePointsByDomainID returns lifecycle=current KPs (with a
// current KU) whose Source is under domainID, excluding excludeSourceID —
// priority-2 fallback "对端 KP 集合" when the new KU has no entry_id
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

// GetPointsByIDs returns lifecycle=current KPs among pointIDs — used by the
// KPN rematch-after-concept-confirm hook (docs/impl/v1/kpn.md 步骤 6,
// RematchPoints).
func (s *Store) GetPointsByIDs(pointIDs []string) ([]KnowledgePoint, error) {
	if len(pointIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pointIDs)), ",")
	args := make([]any, len(pointIDs))
	for i, id := range pointIDs {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT point_id, unit_id, source_id, content, point_type, lifecycle, lifecycle_changed_at, created_at
		FROM knowledge_points WHERE point_id IN (%s) AND lifecycle = 'current'`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get points by ids: %w", err)
	}
	defer rows.Close()
	return scanKnowledgePoints(rows)
}

// GetOrphanPointsByDomain returns the domain's standing entry_id-empty KPs
// (same eligibility as entry.Store.AvailablePoints: current lifecycle on
// both KU and KP, non-shadow Source) for the on-demand "对未归类知识点聚类"
// trigger — unlike GetPointsBySourceID this spans every Source in the
// domain, not just one just-imported Source.
func (s *Store) GetOrphanPointsByDomain(domainID string) ([]KnowledgePoint, error) {
	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.unit_id, kp.source_id, kp.content, kp.point_type, kp.lifecycle, kp.lifecycle_changed_at, kp.created_at
		FROM knowledge_points kp
		INNER JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		INNER JOIN sources s ON s.source_id = kp.source_id
		WHERE ku.entry_id IS NULL AND ku.lifecycle = 'current' AND kp.lifecycle = 'current'
		  AND s.domain_id = ? AND s.shadow_of IS NULL
		ORDER BY kp.created_at ASC`, domainID)
	if err != nil {
		return nil, fmt.Errorf("unit store: get orphan points by domain: %w", err)
	}
	defer rows.Close()
	return scanKnowledgePoints(rows)
}

// GetUnitCentersByIDs returns unit_id -> center for the given units, used to
// enrich the kpn_entry_propose.md prompt (point_id TAB unit_center TAB
// content) when the caller doesn't already have a full KnowledgeUnit slice.
func (s *Store) GetUnitCentersByIDs(unitIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(unitIDs))
	if len(unitIDs) == 0 {
		return result, nil
	}
	seen := make(map[string]bool, len(unitIDs))
	unique := make([]string, 0, len(unitIDs))
	for _, id := range unitIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, len(unique))
	for i, id := range unique {
		args[i] = id
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT unit_id, center FROM knowledge_units WHERE unit_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("unit store: get unit centers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, center string
		if err := rows.Scan(&id, &center); err != nil {
			return nil, fmt.Errorf("unit store: scan unit center: %w", err)
		}
		result[id] = center
	}
	return result, rows.Err()
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

// GetSourceTitleSummary returns sources.title and sources.summary for
// unit_entry_match injection (distinguish source entities, especially facts).
// Missing/empty summary returns "" — callers omit the摘要 field when empty.
func (s *Store) GetSourceTitleSummary(sourceID string) (title, summary string, err error) {
	var sum sql.NullString
	err = s.db.QueryRow(`SELECT title, summary FROM sources WHERE source_id = ?`, sourceID).Scan(&title, &sum)
	if err != nil {
		return "", "", fmt.Errorf("unit store: get source title/summary: %w", err)
	}
	if sum.Valid {
		summary = sum.String
	}
	return title, summary, nil
}

// GetEntriesByDomainID lists candidate entries for unit_entry_match,
// excluding merged_into non-NULL rows — a merged concept is no longer a
// valid match target (docs/impl/v1/concept-evolution.md 步骤 4).
func (s *Store) GetEntriesByDomainID(domainID string) ([]Concept, error) {
	var rows *sql.Rows
	var err error
	const cols = `entry_id, domain_id, name, description, boundary, aliases, kind`
	if domainID != "" {
		rows, err = s.db.Query(`SELECT `+cols+` FROM entries WHERE domain_id = ? AND merged_into IS NULL ORDER BY name`, domainID)
	} else {
		rows, err = s.db.Query(`SELECT ` + cols + ` FROM entries WHERE merged_into IS NULL ORDER BY name`)
	}
	if err != nil {
		return nil, fmt.Errorf("unit store: get entries: %w", err)
	}
	defer rows.Close()

	var entries []Concept
	for rows.Next() {
		var c Concept
		var desc, boundary sql.NullString
		var aliasesJSON string
		if err := rows.Scan(&c.EntryID, &c.DomainID, &c.Name, &desc, &boundary, &aliasesJSON, &c.Kind); err != nil {
			return nil, fmt.Errorf("unit store: scan concept: %w", err)
		}
		if desc.Valid {
			c.Description = desc.String
		}
		if boundary.Valid {
			c.Boundary = boundary.String
		}
		if aliasesJSON != "" {
			// Malformed JSON (should not happen — preset.go always marshals a
			// valid array) degrades to no aliases rather than failing the
			// whole batch.
			_ = json.Unmarshal([]byte(aliasesJSON), &c.Aliases)
		}
		entries = append(entries, c)
	}
	return entries, rows.Err()
}

type Concept struct {
	EntryID     string
	DomainID    string
	Name        string
	Description string
	// Boundary and Aliases surface preset/evolved curation data
	// (migration 044) into unit_entry_match's entry_list so the matcher can
	// use exactly the disambiguation signal these fields were authored for,
	// instead of only name+description.
	Boundary string
	Aliases  []string
	// Kind is the concept/fact classification (entries.kind) — lets callers
	// split the candidate list by kind before matching (docs/impl/v1/kpn.md
	// 步骤 3, 直接匹配链路 kind-aware 改造 2026-08-05).
	Kind string
}

// RerankSemanticsRow is one unit_rerank_semantics row as stored, including
// the manual-curation columns (docs/impl/v1/semantics-curation.md).
type RerankSemanticsRow struct {
	UnitID         string
	SourceTheme    string
	ContentTheme   string
	Intent         string
	Object         string
	Scope          string
	PromptVersion  string
	ManuallyEdited bool
	EditedAt       sql.NullTime
}

// GetRerankSemanticsByUnitID returns the unit's rerank semantics row, or
// (nil, nil) when the unit has no semantics at all — the "missing" state the
// curation UI must surface (被召回会触发 retrieval 完整性报错).
func (s *Store) GetRerankSemanticsByUnitID(unitID string) (*RerankSemanticsRow, error) {
	row := &RerankSemanticsRow{}
	var manuallyEdited int
	err := s.db.QueryRow(`SELECT unit_id, source_theme, content_theme, intent, object, scope, prompt_version, manually_edited, edited_at
		FROM unit_rerank_semantics WHERE unit_id = ?`, unitID).Scan(
		&row.UnitID, &row.SourceTheme, &row.ContentTheme, &row.Intent,
		&row.Object, &row.Scope, &row.PromptVersion,
		&manuallyEdited, &row.EditedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("unit store: get rerank semantics: %w", err)
	}
	row.ManuallyEdited = manuallyEdited != 0
	return row, nil
}

// UpsertManualRerankSemantics writes a human-curated semantics row
// (docs/impl/v1/semantics-curation.md): it sets manually_edited=1 and
// edited_at=now. On update the stored prompt_version is left untouched (it
// records the last LLM extraction, which the manual edit doesn't fake); on
// insert — the unit had no semantics row at all — promptVersion (the current
// rerank.ExtractPromptVersion) is written so the retrieval integrity check
// sees a complete row.
func (s *Store) UpsertManualRerankSemantics(unitID string, sem rerank.Semantics, promptVersion string) error {
	_, err := s.db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, prompt_version, manually_edited, edited_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(unit_id) DO UPDATE SET
			source_theme = excluded.source_theme,
			content_theme = excluded.content_theme,
			intent = excluded.intent,
			object = excluded.object,
			scope = excluded.scope,
			manually_edited = 1,
			edited_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP`,
		unitID, sem.SourceTheme, sem.ContentTheme, sem.Intent,
		sem.Object, sem.Scope, promptVersion)
	if err != nil {
		return fmt.Errorf("unit store: upsert manual semantics: %w", err)
	}
	return nil
}

// GetOutlinePath returns the outline chain root→leaf joined with " / " plus
// the leaf's node_type, for the semantics-curation view（KU 所属目录展示，
// docs/impl/v1/semantics-curation.md）. It walks parent_id upward with a
// depth cap to survive accidental cycles.
func (s *Store) GetOutlinePath(outlineID string) (string, string, error) {
	var titles []string
	var leafNodeType string
	id := outlineID
	for depth := 0; id != "" && depth < 32; depth++ {
		var title, nodeType string
		var parentID sql.NullString
		err := s.db.QueryRow(`SELECT title, node_type, parent_id FROM source_outlines WHERE outline_id = ?`, id).
			Scan(&title, &nodeType, &parentID)
		if err != nil {
			return "", "", fmt.Errorf("unit store: get outline path: %w", err)
		}
		if depth == 0 {
			leafNodeType = nodeType
		}
		titles = append([]string{title}, titles...)
		if !parentID.Valid {
			break
		}
		id = parentID.String
	}
	return strings.Join(titles, " / "), leafNodeType, nil
}
