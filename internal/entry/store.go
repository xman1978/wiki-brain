package entry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// EntryInfo is a entries row's display-relevant subset — the confirm
// UI's "归入已有概念" picker (docs/impl/v1/kpn.md 步骤 6) needs id/name/domain
// only, not the full concept row. Description/KPCount are additionally used
// by the 知识领域 page's concept grid (docs/impl/v1/concept-evolution.md 未定义
// 这块 UI，字段是纯展示性的附加信息，不影响任何匹配/确认逻辑).
type EntryInfo struct {
	EntryID     string
	Name        string
	DomainID    string
	Description string
	Boundary    string
	Kind        string
	KPCount     int
}

// ListActiveEntries returns entries with merged_into IS NULL (still a
// valid entry point), optionally filtered to one domain. Used by the
// concept candidate confirm UI to populate "select an existing concept"
// pickers, and by the 知识领域 page's concept grid.
func (s *Store) ListActiveEntries(domainID string) ([]EntryInfo, error) {
	query := `
		SELECT c.entry_id, c.name, c.domain_id, COALESCE(c.description, ''), COALESCE(c.boundary, ''), c.kind,
			(SELECT COUNT(*) FROM knowledge_points kp
			 JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
			 WHERE ku.entry_id = c.entry_id AND kp.lifecycle = 'current' AND ku.lifecycle = 'current')
		FROM entries c WHERE c.merged_into IS NULL`
	var args []interface{}
	if domainID != "" {
		query += ` AND c.domain_id = ?`
		args = append(args, domainID)
	}
	query += ` ORDER BY c.name ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("concept store: list active entries: %w", err)
	}
	defer rows.Close()

	var results []EntryInfo
	for rows.Next() {
		var c EntryInfo
		if err := rows.Scan(&c.EntryID, &c.Name, &c.DomainID, &c.Description, &c.Boundary, &c.Kind, &c.KPCount); err != nil {
			return nil, fmt.Errorf("concept store: scan concept: %w", err)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

// AvailablePointOption is a knowledge_point row offered by the "add KP" picker
// in the concept candidate confirm dialog — content and source title so the
// UI doesn't need a second round trip per point (mirrors the shape the
// candidate detail view already builds client-side via GET /points/:id +
// GET /sources/:id).
type AvailablePointOption struct {
	PointID     string
	Content     string
	SourceID    string
	SourceTitle string
}

// AvailablePoints lists knowledge_points still eligible for a kind=add
// candidate's point_ids: entry_id IS NULL on their KU (the same
// precondition ConfirmAdd/ConfirmAssign's migration query enforces), current
// lifecycle on both KU and KP, in the given domain, excluding shadow Sources.
// Capped at 200 — the confirm UI is a manual curation aid, not a full browse.
func (s *Store) AvailablePoints(domainID string) ([]AvailablePointOption, error) {
	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.content, s.source_id, s.title
		FROM knowledge_points kp
		JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
		JOIN sources s ON s.source_id = kp.source_id
		WHERE ku.entry_id IS NULL AND ku.lifecycle = 'current' AND kp.lifecycle = 'current'
		  AND s.domain_id = ? AND s.shadow_of IS NULL
		ORDER BY kp.created_at DESC
		LIMIT 200`, domainID)
	if err != nil {
		return nil, fmt.Errorf("concept store: available points: %w", err)
	}
	defer rows.Close()

	var results []AvailablePointOption
	for rows.Next() {
		var p AvailablePointOption
		if err := rows.Scan(&p.PointID, &p.Content, &p.SourceID, &p.SourceTitle); err != nil {
			return nil, fmt.Errorf("concept store: scan available point: %w", err)
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// EntryDetail is a entries row plus its currently-assigned knowledge
// points, for the 知识领域 page's concept detail/edit modal (opened by
// clicking a confirmed concept card — a plain metadata edit, not a
// concept-evolution candidate, so it has no confirm/reject step of its own).
type EntryDetail struct {
	EntryID   string
	DomainID    string
	Name        string
	Description string
	Kind        string
	Points      []AvailablePointOption
	// RestorableCandidateID is the applied kind=add candidate that created
	// this entry (resolved_entry_id points back at it), when Restore
	// supports undoing it — i.e. it created a brand-new entry, not an
	// "assign to existing" or a merge (see Service.Restore's own scope
	// check). Empty when this entry either wasn't created via that path or
	// isn't restorable, so the UI knows when to hide the 撤销 button rather
	// than showing one that will just 400.
	RestorableCandidateID string
}

// GetEntryDetail loads a concept's editable fields plus its current point
// set (via ku.entry_id — the same join AvailablePoints uses in reverse).
// Returns nil, nil if the concept doesn't exist or has been merged away
// (merged_into IS NOT NULL — no longer a valid edit entry point).
func (s *Store) GetEntryDetail(conceptID string) (*EntryDetail, error) {
	var d EntryDetail
	err := s.db.QueryRow(`SELECT entry_id, domain_id, name, COALESCE(description, ''), kind
		FROM entries WHERE entry_id = ? AND merged_into IS NULL`, conceptID,
	).Scan(&d.EntryID, &d.DomainID, &d.Name, &d.Description, &d.Kind)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("concept store: get concept detail: %w", err)
	}

	rows, err := s.db.Query(`
		SELECT kp.point_id, kp.content, s.source_id, s.title
		FROM knowledge_points kp
		JOIN knowledge_units ku ON ku.unit_id = kp.unit_id
		JOIN sources s ON s.source_id = kp.source_id
		WHERE ku.entry_id = ? AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
		ORDER BY kp.created_at DESC`, conceptID)
	if err != nil {
		return nil, fmt.Errorf("concept store: get concept detail: points: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p AvailablePointOption
		if err := rows.Scan(&p.PointID, &p.Content, &p.SourceID, &p.SourceTitle); err != nil {
			return nil, fmt.Errorf("concept store: get concept detail: scan point: %w", err)
		}
		d.Points = append(d.Points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.db.QueryRow(`SELECT candidate_id FROM entry_candidates
		WHERE resolved_entry_id = ? AND status = ? AND kind = ? AND created_new_entry = 1`,
		conceptID, StatusApplied, KindAdd,
	).Scan(&d.RestorableCandidateID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("concept store: get concept detail: restorable candidate: %w", err)
	}

	return &d, nil
}

// UpdateEntryMeta renames/redescribes a concept in place — plain metadata
// editing, distinct from the concept-evolution candidate flow (no structural
// change, no confirm step).
func (s *Store) UpdateEntryMeta(conceptID, name, description, kind string) error {
	res, err := s.db.Exec(`UPDATE entries SET name = ?, description = ?, kind = ? WHERE entry_id = ? AND merged_into IS NULL`,
		name, description, kind, conceptID)
	if err != nil {
		return fmt.Errorf("concept store: update concept meta: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: update concept meta: concept %s not found or merged away", conceptID)
	}
	return nil
}

// AddEntryPoints assigns pointIDs' owning knowledge units onto conceptID,
// same eligibility ConfirmAssign enforces (entry_id IS NULL — only
// still-unclassified units are addable; a unit already in a different
// concept can't be independently reassigned here).
func (s *Store) AddEntryPoints(conceptID string, pointIDs []string) (migrated int, err error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}
	q := fmt.Sprintf(`UPDATE knowledge_units SET entry_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE entry_id IS NULL AND unit_id IN (
			SELECT unit_id FROM knowledge_points WHERE point_id IN (%s)
		)`, placeholders(len(pointIDs)))
	args := append([]interface{}{conceptID}, toArgs(pointIDs)...)
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("concept store: add concept points: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RemoveEntryPoint unassigns pointID's owning knowledge unit from
// conceptID. Note the same granularity constraint the rest of this module
// has always had: entry_id lives on knowledge_units, not on individual
// points, so this clears the whole unit — every other point sharing that
// unit is unclassified too, not just pointID. The handler surfaces this via
// the returned count so the UI can warn when it's more than 1.
func (s *Store) RemoveEntryPoint(conceptID, pointID string) (unitPointCount int, err error) {
	var unitID string
	if err := s.db.QueryRow(`SELECT unit_id FROM knowledge_points WHERE point_id = ?`, pointID).Scan(&unitID); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("concept store: remove concept point: point %s not found", pointID)
		}
		return 0, fmt.Errorf("concept store: remove concept point: %w", err)
	}

	res, err := s.db.Exec(`UPDATE knowledge_units SET entry_id = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE entry_id = ? AND unit_id = ?`, conceptID, unitID)
	if err != nil {
		return 0, fmt.Errorf("concept store: remove concept point: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("concept store: remove concept point: point %s is not currently assigned to concept %s", pointID, conceptID)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_points WHERE unit_id = ?`, unitID).Scan(&unitPointCount); err != nil {
		return 0, fmt.Errorf("concept store: remove concept point: count unit points: %w", err)
	}
	return unitPointCount, nil
}

func placeholders(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = "?"
	}
	return strings.Join(ph, ",")
}

func toArgs(ids []string) []interface{} {
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

func marshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// GapPointEvent is one activation_gap learning_events row whose payload
// classified as gap_level=entry_gap (docs/impl/v1/concept-evolution.md
// activation_gap payload 扩展). The learning_events.processed flag is not
// used to gate this query — that flag's semantics is already owned by
// study.md 步骤 2's link-candidate consumption, which marks every
// activation_gap event processed=1 regardless of gap_level in the same Study
// cycle, before this scan (appended at the task-chain's end) would ever see
// it. Idempotency across cycles instead comes from SeenEventIDs.
type GapPointEvent struct {
	EventID      string
	QuestionHash string
	PointIDs     []string
}

func (s *Store) FetchEntryGapEvents(windowDays int) ([]GapPointEvent, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id, COALESCE(t.question_hash, ''), le.payload
		FROM learning_events le
		LEFT JOIN traces t ON le.trace_id = t.trace_id
		WHERE le.event_type = 'activation_gap'
		  AND le.created_at >= datetime('now', '-' || ? || ' days')
		ORDER BY le.created_at`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("concept store: fetch concept gap events: %w", err)
	}
	defer rows.Close()

	var events []GapPointEvent
	for rows.Next() {
		var eventID, questionHash, payload string
		if err := rows.Scan(&eventID, &questionHash, &payload); err != nil {
			return nil, fmt.Errorf("concept store: scan concept gap event: %w", err)
		}
		var p struct {
			DirectPointIDs []string `json:"direct_point_ids"`
			GapLevel       string   `json:"gap_level"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			continue
		}
		// Legacy events without gap_level default to link_gap
		// (docs/impl/v1/concept-evolution.md 存量事件兼容策略) — not our input.
		if p.GapLevel != "entry_gap" {
			continue
		}
		events = append(events, GapPointEvent{EventID: eventID, QuestionHash: questionHash, PointIDs: p.DirectPointIDs})
	}
	return events, rows.Err()
}

// SeenAddEventIDs returns every learning_event event_id already attributed to
// some kind=add entry_candidates row (any status) — the scan's own
// idempotency marker in place of learning_events.processed (see GapPointEvent).
func (s *Store) SeenAddEventIDs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT event_ids FROM entry_candidates WHERE kind = ?`, KindAdd)
	if err != nil {
		return nil, fmt.Errorf("concept store: seen add event ids: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]bool)
	for rows.Next() {
		var eventIDsJSON string
		if err := rows.Scan(&eventIDsJSON); err != nil {
			return nil, fmt.Errorf("concept store: scan seen event ids: %w", err)
		}
		var ids []string
		if err := json.Unmarshal([]byte(eventIDsJSON), &ids); err != nil {
			continue
		}
		for _, id := range ids {
			seen[id] = true
		}
	}
	return seen, rows.Err()
}

func scanCandidate(row interface{ Scan(...interface{}) error }) (*CandidateRow, error) {
	var c CandidateRow
	if err := row.Scan(&c.CandidateID, &c.Kind, &c.EntryKind, &c.DomainID, &c.SuggestedName, &c.MergeFrom,
		&c.PointIDs, &c.Evidence, &c.EventIDs, &c.Status, &c.LastSignalAt, &c.CreatedAt, &c.UpdatedAt,
		&c.ResolvedEntryID, &c.CreatedNewEntry, &c.KPNRelationIDs); err != nil {
		return nil, err
	}
	return &c, nil
}

const candidateColumns = `candidate_id, kind, entry_kind, domain_id, suggested_name, merge_from,
	point_ids, evidence, event_ids, status, last_signal_at, created_at, updated_at,
	resolved_entry_id, created_new_entry, kpn_relation_ids`

func (s *Store) ListCandidatesByKindStatus(kind, status string) ([]CandidateRow, error) {
	rows, err := s.db.Query(`SELECT `+candidateColumns+` FROM entry_candidates
		WHERE kind = ? AND status = ? ORDER BY last_signal_at DESC`, kind, status)
	if err != nil {
		return nil, fmt.Errorf("concept store: list candidates by kind/status: %w", err)
	}
	defer rows.Close()

	var results []CandidateRow
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("concept store: scan candidate: %w", err)
		}
		results = append(results, *c)
	}
	return results, rows.Err()
}

// ListDomainAddCandidates returns kind=add candidates targeting domainID that
// are still relevant for the 知识领域 page's concept grid: pending_confirm
// (needs a decision), rejected/expired (kept visible as a status badge on
// their card, per the merged-grid design — there is no separate list view
// anymore). Applied candidates are excluded: those already became real
// entries rows, which ListActiveEntries covers instead.
func (s *Store) ListDomainAddCandidates(domainID string) ([]CandidateRow, error) {
	rows, err := s.db.Query(`SELECT `+candidateColumns+` FROM entry_candidates
		WHERE kind = ? AND domain_id = ? AND status IN (?, ?, ?)
		ORDER BY updated_at DESC`,
		KindAdd, domainID, StatusPendingConfirm, StatusRejected, StatusExpired)
	if err != nil {
		return nil, fmt.Errorf("concept store: list domain add candidates: %w", err)
	}
	defer rows.Close()

	var results []CandidateRow
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("concept store: scan candidate: %w", err)
		}
		results = append(results, *c)
	}
	return results, rows.Err()
}

func (s *Store) ListCandidates(status string) ([]CandidateRow, error) {
	query := `SELECT ` + candidateColumns + ` FROM entry_candidates`
	var args []interface{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY last_signal_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("concept store: list candidates: %w", err)
	}
	defer rows.Close()

	var results []CandidateRow
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("concept store: scan candidate: %w", err)
		}
		results = append(results, *c)
	}
	return results, rows.Err()
}

func (s *Store) GetCandidate(candidateID string) (*CandidateRow, error) {
	row := s.db.QueryRow(`SELECT `+candidateColumns+` FROM entry_candidates WHERE candidate_id = ?`, candidateID)
	c, err := scanCandidate(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("concept store: get candidate: %w", err)
	}
	return c, nil
}

// InsertAddCandidate creates a new kind=add row plus its pending_confirm
// entry_add_candidate learning_result (docs/impl/v1/concept-evolution.md
// 步骤 2).
func (s *Store) InsertAddCandidate(domainID sql.NullString, suggestedName, conceptKind string, pointIDs, eventIDs []string, evidence interface{}, reason string) (string, error) {
	candidateID := uuid.New().String()
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("concept store: insert add candidate: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO entry_candidates
		(candidate_id, kind, entry_kind, domain_id, suggested_name, merge_from, point_ids, evidence, event_ids, status, last_signal_at)
		VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?)`,
		candidateID, KindAdd, conceptKind, domainID, suggestedName, marshal(pointIDs), marshal(evidence), marshal(eventIDs), StatusPendingConfirm, now); err != nil {
		return "", fmt.Errorf("concept store: insert add candidate: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), activation.ActionEntryAddCandidate, activation.ObjectTypeEntryCandidate, candidateID, reason, marshal(eventIDs), activation.ResultPendingConfirm); err != nil {
		return "", fmt.Errorf("concept store: insert add candidate learning result: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("concept store: insert add candidate: commit: %w", err)
	}
	return candidateID, nil
}

// UpdateCandidateSignal merges newly-seen event_ids into an existing
// pending_confirm candidate's evidence/event_ids/last_signal_at without
// touching point_ids (docs/impl/v1/concept-evolution.md 步骤 2:
// "更新该候选的 evidence / event_ids / last_signal_at，不重复建行").
func (s *Store) UpdateCandidateSignal(candidateID string, evidence interface{}, newEventIDs []string) error {
	existing, err := s.GetCandidate(candidateID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("concept store: update candidate signal: candidate not found: %s", candidateID)
	}

	var eventIDs []string
	if err := json.Unmarshal([]byte(existing.EventIDs), &eventIDs); err != nil {
		eventIDs = nil
	}
	seen := make(map[string]bool, len(eventIDs))
	for _, id := range eventIDs {
		seen[id] = true
	}
	for _, id := range newEventIDs {
		if !seen[id] {
			eventIDs = append(eventIDs, id)
			seen[id] = true
		}
	}

	_, err = s.db.Exec(`UPDATE entry_candidates SET evidence = ?, event_ids = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ?`, marshal(evidence), marshal(eventIDs), candidateID)
	if err != nil {
		return fmt.Errorf("concept store: update candidate signal: %w", err)
	}
	return nil
}

// MergeAddCandidatePoints appends morePointIDs (deduped) into an existing
// pending_confirm kind=add candidate's point_ids and refreshes its
// evidence/last_signal_at/updated_at, without touching suggested_name
// (docs/impl/v1/kpn.md 步骤 3: "point_ids 并入该候选...不重复建行", extending
// the same no-duplicate-row principle from usage-driven candidates to
// content-driven ones).
func (s *Store) MergeAddCandidatePoints(candidateID string, morePointIDs []string, evidence interface{}) error {
	existing, err := s.GetCandidate(candidateID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("concept store: merge add candidate points: candidate not found: %s", candidateID)
	}

	var pointIDs []string
	if err := json.Unmarshal([]byte(existing.PointIDs), &pointIDs); err != nil {
		pointIDs = nil
	}
	seen := make(map[string]bool, len(pointIDs))
	for _, id := range pointIDs {
		seen[id] = true
	}
	for _, id := range morePointIDs {
		if !seen[id] {
			pointIDs = append(pointIDs, id)
			seen[id] = true
		}
	}

	_, err = s.db.Exec(`UPDATE entry_candidates SET point_ids = ?, evidence = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ?`, marshal(pointIDs), marshal(evidence), candidateID)
	if err != nil {
		return fmt.Errorf("concept store: merge add candidate points: %w", err)
	}
	return nil
}

// ConceptActive reports whether conceptID exists and has not been merged
// away (merged_into IS NULL) — the validity check for "归入已有概念"
// (docs/impl/v1/kpn.md 步骤 6).
func (s *Store) ConceptActive(conceptID string) (bool, error) {
	var mergedInto sql.NullString
	err := s.db.QueryRow(`SELECT merged_into FROM entries WHERE entry_id = ?`, conceptID).Scan(&mergedInto)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("concept store: concept active: %w", err)
	}
	return !mergedInto.Valid, nil
}

// ConfirmAssign executes a kind=add candidate by assigning its point_ids to
// an already-existing concept instead of creating a new one
// (docs/impl/v1/kpn.md 步骤 6 "归入已有概念") — for content_driven candidates
// that are really a unit_entry_match miss rather than a genuine taxonomy
// gap, avoiding unnecessary concept-table growth.
func (s *Store) ConfirmAssign(candidateID, conceptID string, pointIDs []string, reason string) (migratedKUs int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("concept store: confirm assign: begin: %w", err)
	}
	defer tx.Rollback()

	if len(pointIDs) > 0 {
		q := fmt.Sprintf(`UPDATE knowledge_units SET entry_id = ?, updated_at = CURRENT_TIMESTAMP
			WHERE entry_id IS NULL AND unit_id IN (
				SELECT unit_id FROM knowledge_points WHERE point_id IN (%s)
			)`, placeholders(len(pointIDs)))
		args := append([]interface{}{conceptID}, toArgs(pointIDs)...)
		res, err := tx.Exec(q, args...)
		if err != nil {
			return 0, fmt.Errorf("concept store: confirm assign: migrate units: %w", err)
		}
		n, _ := res.RowsAffected()
		migratedKUs = int(n)
	}

	// Keep the candidate row's own point_ids in sync — the confirm request
	// may have added/removed KPs from the original suggestion (concept
	// candidate confirm dialog's KP picker), and the candidate list/detail
	// view reads this column, not a live migration query.
	// created_new_entry stays 0: this candidate didn't create conceptID,
	// so it isn't eligible for the "restore applied to pending" rollback
	// (that only undoes a candidate's own new-concept creation).
	if _, err := tx.Exec(`UPDATE entry_candidates SET point_ids = ?, resolved_entry_id = ? WHERE candidate_id = ?`,
		marshal(pointIDs), conceptID, candidateID); err != nil {
		return 0, fmt.Errorf("concept store: confirm assign: sync point_ids: %w", err)
	}

	if err := resolveCandidate(tx, candidateID, StatusApplied, reason); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("concept store: confirm assign: commit: %w", err)
	}
	return migratedKUs, nil
}

// InsertMergeCandidate creates a new kind=merge row plus its pending_confirm
// entry_merge_candidate learning_result.
func (s *Store) InsertMergeCandidate(mergeFrom []string, pointIDs []string, evidence MergeEvidence, reason string) (string, error) {
	candidateID := uuid.New().String()
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO entry_candidates
		(candidate_id, kind, domain_id, suggested_name, merge_from, point_ids, evidence, event_ids, status, last_signal_at)
		VALUES (?, ?, NULL, NULL, ?, ?, ?, '[]', ?, ?)`,
		candidateID, KindMerge, marshal(mergeFrom), marshal(pointIDs), marshal(evidence), StatusPendingConfirm, now); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status)
		VALUES (?, ?, ?, ?, ?, '[]', ?)`,
		uuid.New().String(), activation.ActionEntryMergeCandidate, activation.ObjectTypeEntryCandidate, candidateID, reason, activation.ResultPendingConfirm); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate learning result: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: commit: %w", err)
	}
	return candidateID, nil
}

func (s *Store) UpdateMergeCandidateSignal(candidateID string, evidence MergeEvidence) error {
	_, err := s.db.Exec(`UPDATE entry_candidates SET evidence = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ?`, marshal(evidence), candidateID)
	if err != nil {
		return fmt.Errorf("concept store: update merge candidate signal: %w", err)
	}
	return nil
}

// ExpireIdleCandidates flips pending_confirm candidates idle past idleDays to
// expired, mirroring the same status onto their learning_results row
// (docs/impl/v1/concept-evolution.md 步骤 2 过期).
func (s *Store) ExpireIdleCandidates(idleDays int) ([]string, error) {
	rows, err := s.db.Query(`SELECT candidate_id FROM entry_candidates
		WHERE status = ? AND last_signal_at < datetime('now', '-' || ? || ' days')`, StatusPendingConfirm, idleDays)
	if err != nil {
		return nil, fmt.Errorf("concept store: find idle candidates: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("concept store: scan idle candidate: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, id := range ids {
		if err := s.expireCandidate(id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *Store) expireCandidate(candidateID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("concept store: expire candidate: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE entry_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE candidate_id = ?`,
		StatusExpired, candidateID); err != nil {
		return fmt.Errorf("concept store: expire candidate: %w", err)
	}
	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'auto', updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultExpired, activation.ObjectTypeEntryCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
		return fmt.Errorf("concept store: expire candidate learning result: %w", err)
	}
	return tx.Commit()
}

// Reject marks a pending_confirm candidate rejected without any structural
// change (docs/impl/v1/concept-evolution.md 步骤 3).
func (s *Store) Reject(candidateID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("concept store: reject: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE entry_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, StatusRejected, candidateID, StatusPendingConfirm)
	if err != nil {
		return fmt.Errorf("concept store: reject: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: reject: candidate %s not pending_confirm", candidateID)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultRejected, activation.ObjectTypeEntryCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
		return fmt.Errorf("concept store: reject learning result: %w", err)
	}

	return tx.Commit()
}

// DeleteCandidate hard-deletes a pending_confirm candidate and its
// learning_result row — unlike Reject, nothing is kept around to show up in
// any tab afterward. Safe because a pending_confirm candidate has never
// touched concept/KU data (that only happens on Confirm), so there is
// nothing structural to undo.
func (s *Store) DeleteCandidate(candidateID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("concept store: delete candidate: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM entry_candidates WHERE candidate_id = ? AND status = ?`, candidateID, StatusPendingConfirm)
	if err != nil {
		return fmt.Errorf("concept store: delete candidate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: delete candidate: %s not pending_confirm", candidateID)
	}

	if _, err := tx.Exec(`DELETE FROM learning_results WHERE object_type = ? AND object_id = ?`,
		activation.ObjectTypeEntryCandidate, candidateID); err != nil {
		return fmt.Errorf("concept store: delete candidate learning result: %w", err)
	}

	return tx.Commit()
}

// RestoreRejected flips a rejected candidate back to pending_confirm. Reject
// never mutated concept/KU data, so this is a pure status flip — safe for
// either kind (add or merge).
func (s *Store) RestoreRejected(candidateID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("concept store: restore rejected: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE entry_candidates SET status = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, StatusPendingConfirm, candidateID, StatusRejected)
	if err != nil {
		return fmt.Errorf("concept store: restore rejected: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: restore rejected: candidate %s not rejected", candidateID)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', reason = ?, updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultPendingConfirm, "人工从已驳回恢复至待确认", activation.ObjectTypeEntryCandidate, candidateID, activation.ResultRejected); err != nil {
		return fmt.Errorf("concept store: restore rejected learning result: %w", err)
	}

	return tx.Commit()
}

// RestoreAppliedNewEntry undoes an applied kind=add candidate that created
// a brand-new concept: reverts pointIDs' KUs to entry_id=NULL, deletes the
// KPN cross-Source relations this candidate's own directed rematch created
// (relationIDs — see RematchPoints/recordKPNRelationIDs; a later, unrelated
// Source import's own relations are never touched, since only this
// candidate's own recorded ids are targeted), then deletes the concept row
// — refusing if any other KU still references it (e.g. a later "归入已有概念"
// confirm assigned more KPs onto it after creation), since deleting would
// then orphan that reference.
func (s *Store) RestoreAppliedNewEntry(candidateID, conceptID string, pointIDs, relationIDs []string, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("concept store: restore applied: begin: %w", err)
	}
	defer tx.Rollback()

	if len(pointIDs) > 0 {
		q := fmt.Sprintf(`UPDATE knowledge_units SET entry_id = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE entry_id = ? AND unit_id IN (
				SELECT unit_id FROM knowledge_points WHERE point_id IN (%s)
			)`, placeholders(len(pointIDs)))
		args := append([]interface{}{conceptID}, toArgs(pointIDs)...)
		if _, err := tx.Exec(q, args...); err != nil {
			return fmt.Errorf("concept store: restore applied: revert units: %w", err)
		}
	}

	var remaining int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_units WHERE entry_id = ?`, conceptID).Scan(&remaining); err != nil {
		return fmt.Errorf("concept store: restore applied: check remaining units: %w", err)
	}
	if remaining > 0 {
		return fmt.Errorf("concept store: restore applied: concept %s still has %d knowledge unit(s) referencing it (likely assigned by a later confirm) — resolve those first", conceptID, remaining)
	}

	if len(relationIDs) > 0 {
		q := fmt.Sprintf(`DELETE FROM knowledge_point_relations WHERE relation_id IN (%s)`, placeholders(len(relationIDs)))
		if _, err := tx.Exec(q, toArgs(relationIDs)...); err != nil {
			return fmt.Errorf("concept store: restore applied: delete kpn relations: %w", err)
		}
	}

	// Clear the candidate's own FK reference before deleting the concept row
	// it points to — entry_candidates.resolved_entry_id REFERENCES
	// entries(entry_id), so this must happen first.
	res, err := tx.Exec(`UPDATE entry_candidates SET status = ?, resolved_entry_id = NULL, created_new_entry = 0, kpn_relation_ids = '[]',
		last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, StatusPendingConfirm, candidateID, StatusApplied)
	if err != nil {
		return fmt.Errorf("concept store: restore applied: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: restore applied: candidate %s not applied", candidateID)
	}

	if _, err := tx.Exec(`DELETE FROM entries WHERE entry_id = ?`, conceptID); err != nil {
		return fmt.Errorf("concept store: restore applied: delete concept: %w", err)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', reason = ?, updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultPendingConfirm, reason, activation.ObjectTypeEntryCandidate, candidateID, activation.ResultApplied); err != nil {
		return fmt.Errorf("concept store: restore applied learning result: %w", err)
	}

	return tx.Commit()
}

// SetCandidateKPNRelationIDs records the relation_ids RematchPoints created
// for a candidate's confirm, appending to whatever was already recorded
// (RematchPoints can run in multiple source-grouped batches per confirm —
// see recordKPNRelationIDs, called once per confirm with that call's full
// result, so in practice this always overwrites '[]' with the complete set,
// but appends defensively rather than assuming call order).
func (s *Store) SetCandidateKPNRelationIDs(candidateID string, relationIDs []string) error {
	var existing string
	if err := s.db.QueryRow(`SELECT kpn_relation_ids FROM entry_candidates WHERE candidate_id = ?`, candidateID).Scan(&existing); err != nil {
		return fmt.Errorf("concept store: set kpn relation ids: read existing: %w", err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(existing), &ids); err != nil {
		return fmt.Errorf("concept store: set kpn relation ids: unmarshal existing: %w", err)
	}
	ids = append(ids, relationIDs...)
	if _, err := s.db.Exec(`UPDATE entry_candidates SET kpn_relation_ids = ? WHERE candidate_id = ?`, marshal(ids), candidateID); err != nil {
		return fmt.Errorf("concept store: set kpn relation ids: %w", err)
	}
	return nil
}

// ConfirmAdd executes a kind=add candidate in a single transaction
// (docs/impl/v1/concept-evolution.md 步骤 3): create the concept
// (origin=evolved), migrate every entry_id-NULL KU behind pointIDs onto it,
// and resolve the candidate + its learning_result to applied.
func (s *Store) ConfirmAdd(candidateID, conceptID, domainID, name, description, boundary, kind string, aliases, pointIDs []string, reason string) (migratedKUs int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("concept store: confirm add: begin: %w", err)
	}
	defer tx.Rollback()

	if aliases == nil {
		aliases = []string{}
	}
	if _, err := tx.Exec(`INSERT INTO entries (entry_id, domain_id, name, description, kind, origin, boundary, aliases) VALUES (?, ?, ?, ?, ?, 'evolved', ?, ?)`,
		conceptID, domainID, name, description, kind, boundary, marshal(aliases)); err != nil {
		return 0, fmt.Errorf("concept store: confirm add: insert concept: %w", err)
	}

	// Keep the candidate row's own suggested_name/point_ids in sync with what
	// was actually used to create the concept — the confirm request may have
	// overridden the LLM's original suggestion and/or added/removed KPs via
	// the confirm dialog's picker, and the candidate list/detail view reads
	// these columns, not entries.name or a live migration query.
	// created_new_entry = 1: this confirm is the one that created conceptID,
	// so it's the only path eligible for RestoreApplied's rollback.
	if _, err := tx.Exec(`UPDATE entry_candidates SET suggested_name = ?, entry_kind = ?, point_ids = ?, resolved_entry_id = ?, created_new_entry = 1 WHERE candidate_id = ?`,
		name, kind, marshal(pointIDs), conceptID, candidateID); err != nil {
		return 0, fmt.Errorf("concept store: confirm add: sync suggested_name/point_ids: %w", err)
	}

	if len(pointIDs) > 0 {
		q := fmt.Sprintf(`UPDATE knowledge_units SET entry_id = ?, updated_at = CURRENT_TIMESTAMP
			WHERE entry_id IS NULL AND unit_id IN (
				SELECT unit_id FROM knowledge_points WHERE point_id IN (%s)
			)`, placeholders(len(pointIDs)))
		args := append([]interface{}{conceptID}, toArgs(pointIDs)...)
		res, err := tx.Exec(q, args...)
		if err != nil {
			return 0, fmt.Errorf("concept store: confirm add: migrate units: %w", err)
		}
		n, _ := res.RowsAffected()
		migratedKUs = int(n)
	}

	if err := resolveCandidate(tx, candidateID, StatusApplied, reason); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("concept store: confirm add: commit: %w", err)
	}
	return migratedKUs, nil
}

// ConfirmMerge executes a kind=merge candidate in a single transaction
// (docs/impl/v1/concept-evolution.md 步骤 3): every non-target concept in
// mergeFrom has its KUs' entry_id repointed to target and gets
// merged_into=target; wiki needs_recompile flagging happens post-commit by
// the caller (service layer), since it goes through the Wiki module's own
// interface rather than raw SQL in this transaction.
func (s *Store) ConfirmMerge(candidateID string, mergeFrom []string, target, reason string) (migratedKUs int, mergedAway []string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("concept store: confirm merge: begin: %w", err)
	}
	defer tx.Rollback()

	for _, other := range mergeFrom {
		if other == target {
			continue
		}
		res, err := tx.Exec(`UPDATE knowledge_units SET entry_id = ?, updated_at = CURRENT_TIMESTAMP WHERE entry_id = ?`, target, other)
		if err != nil {
			return 0, nil, fmt.Errorf("concept store: confirm merge: migrate units: %w", err)
		}
		n, _ := res.RowsAffected()
		migratedKUs += int(n)

		if _, err := tx.Exec(`UPDATE entries SET merged_into = ? WHERE entry_id = ?`, target, other); err != nil {
			return 0, nil, fmt.Errorf("concept store: confirm merge: mark merged_into: %w", err)
		}
		mergedAway = append(mergedAway, other)
	}

	if err := resolveCandidate(tx, candidateID, StatusApplied, reason); err != nil {
		return 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("concept store: confirm merge: commit: %w", err)
	}
	return migratedKUs, mergedAway, nil
}

func resolveCandidate(tx *sql.Tx, candidateID, status, reason string) error {
	res, err := tx.Exec(`UPDATE entry_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, status, candidateID, StatusPendingConfirm)
	if err != nil {
		return fmt.Errorf("concept store: resolve candidate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: resolve candidate: %s not pending_confirm", candidateID)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', reason = ?, updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultApplied, reason, activation.ObjectTypeEntryCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
		return fmt.Errorf("concept store: resolve candidate learning result: %w", err)
	}
	return nil
}

// KUInfo is a KnowledgeUnit's fields relevant to add-cluster domain-vote and
// suggested-name derivation.
type KUInfo struct {
	UnitID    string
	Center    string
	SourceID  string
	EntryID sql.NullString
}

// KUInfoForPoints maps each of pointIDs to its owning KnowledgeUnit's info.
func (s *Store) KUInfoForPoints(pointIDs []string) (map[string]KUInfo, error) {
	if len(pointIDs) == 0 {
		return map[string]KUInfo{}, nil
	}
	q := fmt.Sprintf(`SELECT kp.point_id, ku.unit_id, ku.center, ku.source_id, ku.entry_id
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE kp.point_id IN (%s)`, placeholders(len(pointIDs)))
	rows, err := s.db.Query(q, toArgs(pointIDs)...)
	if err != nil {
		return nil, fmt.Errorf("concept store: ku info for points: %w", err)
	}
	defer rows.Close()

	result := make(map[string]KUInfo, len(pointIDs))
	for rows.Next() {
		var pointID string
		var info KUInfo
		if err := rows.Scan(&pointID, &info.UnitID, &info.Center, &info.SourceID, &info.EntryID); err != nil {
			return nil, fmt.Errorf("concept store: scan ku info: %w", err)
		}
		result[pointID] = info
	}
	return result, rows.Err()
}

// SourceDomains maps each of sourceIDs to its domain_id (nullable).
func (s *Store) SourceDomains(sourceIDs []string) (map[string]sql.NullString, error) {
	if len(sourceIDs) == 0 {
		return map[string]sql.NullString{}, nil
	}
	q := fmt.Sprintf(`SELECT source_id, domain_id FROM sources WHERE source_id IN (%s)`, placeholders(len(sourceIDs)))
	rows, err := s.db.Query(q, toArgs(sourceIDs)...)
	if err != nil {
		return nil, fmt.Errorf("concept store: source domains: %w", err)
	}
	defer rows.Close()

	result := make(map[string]sql.NullString, len(sourceIDs))
	for rows.Next() {
		var sourceID string
		var domainID sql.NullString
		if err := rows.Scan(&sourceID, &domainID); err != nil {
			return nil, fmt.Errorf("concept store: scan source domain: %w", err)
		}
		result[sourceID] = domainID
	}
	return result, rows.Err()
}

// TraceDirectPoints is one traces row's direct_point_ids, for the merge scan
// (docs/impl/v1/concept-evolution.md 步骤 2 合并统计).
type TraceDirectPoints struct {
	TraceID  string
	PointIDs []string
}

func (s *Store) TracesInWindow(windowDays int) ([]TraceDirectPoints, error) {
	rows, err := s.db.Query(`SELECT trace_id, direct_point_ids FROM traces
		WHERE created_at >= datetime('now', '-' || ? || ' days')`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("concept store: traces in window: %w", err)
	}
	defer rows.Close()

	var results []TraceDirectPoints
	for rows.Next() {
		var traceID, pointIDsJSON string
		if err := rows.Scan(&traceID, &pointIDsJSON); err != nil {
			return nil, fmt.Errorf("concept store: scan trace: %w", err)
		}
		var pointIDs []string
		if err := json.Unmarshal([]byte(pointIDsJSON), &pointIDs); err != nil {
			continue
		}
		if len(pointIDs) == 0 {
			continue
		}
		results = append(results, TraceDirectPoints{TraceID: traceID, PointIDs: pointIDs})
	}
	return results, rows.Err()
}

// PointEntryMap maps each of pointIDs to its entry_id, excluding points
// whose KU has no entry_id or whose concept has been merged_into another
// (docs/impl/v1/concept-evolution.md 步骤 2 合并统计: "排除 merged_into 非空的
// 概念参与统计").
func (s *Store) PointEntryMap(pointIDs []string) (map[string]string, error) {
	if len(pointIDs) == 0 {
		return map[string]string{}, nil
	}
	q := fmt.Sprintf(`SELECT kp.point_id, ku.entry_id
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN entries c ON ku.entry_id = c.entry_id
		WHERE kp.point_id IN (%s) AND ku.entry_id IS NOT NULL AND c.merged_into IS NULL`, placeholders(len(pointIDs)))
	rows, err := s.db.Query(q, toArgs(pointIDs)...)
	if err != nil {
		return nil, fmt.Errorf("concept store: point concept map: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string, len(pointIDs))
	for rows.Next() {
		var pointID, conceptID string
		if err := rows.Scan(&pointID, &conceptID); err != nil {
			return nil, fmt.Errorf("concept store: scan point concept: %w", err)
		}
		result[pointID] = conceptID
	}
	return result, rows.Err()
}
