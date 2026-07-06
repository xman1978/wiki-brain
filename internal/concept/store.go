package concept

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
// classified as gap_level=concept_gap (docs/impl/v1/concept-evolution.md
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

func (s *Store) FetchConceptGapEvents(windowDays int) ([]GapPointEvent, error) {
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
		if p.GapLevel != "concept_gap" {
			continue
		}
		events = append(events, GapPointEvent{EventID: eventID, QuestionHash: questionHash, PointIDs: p.DirectPointIDs})
	}
	return events, rows.Err()
}

// SeenAddEventIDs returns every learning_event event_id already attributed to
// some kind=add concept_candidates row (any status) — the scan's own
// idempotency marker in place of learning_events.processed (see GapPointEvent).
func (s *Store) SeenAddEventIDs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT event_ids FROM concept_candidates WHERE kind = ?`, KindAdd)
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
	if err := row.Scan(&c.CandidateID, &c.Kind, &c.DomainID, &c.SuggestedName, &c.MergeFrom,
		&c.PointIDs, &c.Evidence, &c.EventIDs, &c.Status, &c.LastSignalAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

const candidateColumns = `candidate_id, kind, domain_id, suggested_name, merge_from,
	point_ids, evidence, event_ids, status, last_signal_at, created_at, updated_at`

func (s *Store) ListCandidatesByKindStatus(kind, status string) ([]CandidateRow, error) {
	rows, err := s.db.Query(`SELECT `+candidateColumns+` FROM concept_candidates
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

func (s *Store) ListCandidates(status string) ([]CandidateRow, error) {
	query := `SELECT ` + candidateColumns + ` FROM concept_candidates`
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
	row := s.db.QueryRow(`SELECT `+candidateColumns+` FROM concept_candidates WHERE candidate_id = ?`, candidateID)
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
// concept_add_candidate learning_result (docs/impl/v1/concept-evolution.md
// 步骤 2).
func (s *Store) InsertAddCandidate(domainID sql.NullString, suggestedName string, pointIDs, eventIDs []string, evidence AddEvidence, reason string) (string, error) {
	candidateID := uuid.New().String()
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("concept store: insert add candidate: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO concept_candidates
		(candidate_id, kind, domain_id, suggested_name, merge_from, point_ids, evidence, event_ids, status, last_signal_at)
		VALUES (?, ?, ?, ?, '[]', ?, ?, ?, ?, ?)`,
		candidateID, KindAdd, domainID, suggestedName, marshal(pointIDs), marshal(evidence), marshal(eventIDs), StatusPendingConfirm, now); err != nil {
		return "", fmt.Errorf("concept store: insert add candidate: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), activation.ActionConceptAddCandidate, activation.ObjectTypeConceptCandidate, candidateID, reason, marshal(eventIDs), activation.ResultPendingConfirm); err != nil {
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

	_, err = s.db.Exec(`UPDATE concept_candidates SET evidence = ?, event_ids = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ?`, marshal(evidence), marshal(eventIDs), candidateID)
	if err != nil {
		return fmt.Errorf("concept store: update candidate signal: %w", err)
	}
	return nil
}

// InsertMergeCandidate creates a new kind=merge row plus its pending_confirm
// concept_merge_candidate learning_result.
func (s *Store) InsertMergeCandidate(mergeFrom []string, pointIDs []string, evidence MergeEvidence, reason string) (string, error) {
	candidateID := uuid.New().String()
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO concept_candidates
		(candidate_id, kind, domain_id, suggested_name, merge_from, point_ids, evidence, event_ids, status, last_signal_at)
		VALUES (?, ?, NULL, NULL, ?, ?, ?, '[]', ?, ?)`,
		candidateID, KindMerge, marshal(mergeFrom), marshal(pointIDs), marshal(evidence), StatusPendingConfirm, now); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO learning_results
		(result_id, action, object_type, object_id, reason, event_ids, status)
		VALUES (?, ?, ?, ?, ?, '[]', ?)`,
		uuid.New().String(), activation.ActionConceptMergeCandidate, activation.ObjectTypeConceptCandidate, candidateID, reason, activation.ResultPendingConfirm); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate learning result: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("concept store: insert merge candidate: commit: %w", err)
	}
	return candidateID, nil
}

func (s *Store) UpdateMergeCandidateSignal(candidateID string, evidence MergeEvidence) error {
	_, err := s.db.Exec(`UPDATE concept_candidates SET evidence = ?, last_signal_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
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
	rows, err := s.db.Query(`SELECT candidate_id FROM concept_candidates
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

	if _, err := tx.Exec(`UPDATE concept_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE candidate_id = ?`,
		StatusExpired, candidateID); err != nil {
		return fmt.Errorf("concept store: expire candidate: %w", err)
	}
	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'auto', updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultExpired, activation.ObjectTypeConceptCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
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

	res, err := tx.Exec(`UPDATE concept_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, StatusRejected, candidateID, StatusPendingConfirm)
	if err != nil {
		return fmt.Errorf("concept store: reject: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: reject: candidate %s not pending_confirm", candidateID)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultRejected, activation.ObjectTypeConceptCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
		return fmt.Errorf("concept store: reject learning result: %w", err)
	}

	return tx.Commit()
}

// ConfirmAdd executes a kind=add candidate in a single transaction
// (docs/impl/v1/concept-evolution.md 步骤 3): create the concept
// (origin=evolved), migrate every concept_id-NULL KU behind pointIDs onto it,
// and resolve the candidate + its learning_result to applied.
func (s *Store) ConfirmAdd(candidateID, conceptID, domainID, name, description string, pointIDs []string, reason string) (migratedKUs int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("concept store: confirm add: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO concepts (concept_id, domain_id, name, description, origin) VALUES (?, ?, ?, ?, 'evolved')`,
		conceptID, domainID, name, description); err != nil {
		return 0, fmt.Errorf("concept store: confirm add: insert concept: %w", err)
	}

	if len(pointIDs) > 0 {
		q := fmt.Sprintf(`UPDATE knowledge_units SET concept_id = ?, updated_at = CURRENT_TIMESTAMP
			WHERE concept_id IS NULL AND unit_id IN (
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
// mergeFrom has its KUs' concept_id repointed to target and gets
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
		res, err := tx.Exec(`UPDATE knowledge_units SET concept_id = ?, updated_at = CURRENT_TIMESTAMP WHERE concept_id = ?`, target, other)
		if err != nil {
			return 0, nil, fmt.Errorf("concept store: confirm merge: migrate units: %w", err)
		}
		n, _ := res.RowsAffected()
		migratedKUs += int(n)

		if _, err := tx.Exec(`UPDATE concepts SET merged_into = ? WHERE concept_id = ?`, target, other); err != nil {
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
	res, err := tx.Exec(`UPDATE concept_candidates SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE candidate_id = ? AND status = ?`, status, candidateID, StatusPendingConfirm)
	if err != nil {
		return fmt.Errorf("concept store: resolve candidate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("concept store: resolve candidate: %s not pending_confirm", candidateID)
	}

	if _, err := tx.Exec(`UPDATE learning_results SET status = ?, confirmed_by = 'manual', reason = ?, updated_at = CURRENT_TIMESTAMP
		WHERE object_type = ? AND object_id = ? AND status = ?`,
		activation.ResultApplied, reason, activation.ObjectTypeConceptCandidate, candidateID, activation.ResultPendingConfirm); err != nil {
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
	ConceptID sql.NullString
}

// KUInfoForPoints maps each of pointIDs to its owning KnowledgeUnit's info.
func (s *Store) KUInfoForPoints(pointIDs []string) (map[string]KUInfo, error) {
	if len(pointIDs) == 0 {
		return map[string]KUInfo{}, nil
	}
	q := fmt.Sprintf(`SELECT kp.point_id, ku.unit_id, ku.center, ku.source_id, ku.concept_id
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
		if err := rows.Scan(&pointID, &info.UnitID, &info.Center, &info.SourceID, &info.ConceptID); err != nil {
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

// PointConceptMap maps each of pointIDs to its concept_id, excluding points
// whose KU has no concept_id or whose concept has been merged_into another
// (docs/impl/v1/concept-evolution.md 步骤 2 合并统计: "排除 merged_into 非空的
// 概念参与统计").
func (s *Store) PointConceptMap(pointIDs []string) (map[string]string, error) {
	if len(pointIDs) == 0 {
		return map[string]string{}, nil
	}
	q := fmt.Sprintf(`SELECT kp.point_id, ku.concept_id
		FROM knowledge_points kp
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN concepts c ON ku.concept_id = c.concept_id
		WHERE kp.point_id IN (%s) AND ku.concept_id IS NOT NULL AND c.merged_into IS NULL`, placeholders(len(pointIDs)))
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
