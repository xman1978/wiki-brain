package study

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/graph"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Step 1: ScanCandidates aggregates cooccurrence per point_id and upserts one
// link_candidates row per qualifying point, under that point's representative
// label with the aggregated counts.
//
// Aggregation rationale (docs/impl/v1/study.md 步骤 1, 2026-07-18 修订)：
// question_terms 的取值是 session 解析出的 subject 标签，同一话题的不同问法会
// 产生不同标签（"数据库句柄限制"/"数据库句柄管理"……），按 (label, point) 行级
// 计数会把同一 KP 的学习信号打散在多行、每行都到不了阈值；KP 本身才是稳定锚点，
// 达标判定改在 point 级聚合上做。代表标签取 confident_count 最高（并列取
// last_seen_at 最新）的一行，作为候选与后续链接的 question_terms。
func (s *Store) ScanCandidates(confidentMin int, ratioMin float64, batchSize int) (int, error) {
	rows, err := s.db.Query(`
		SELECT point_id, SUM(confident_count), SUM(hit_count)
		FROM question_kp_cooccurrence
		GROUP BY point_id
		HAVING SUM(confident_count) >= ?
		  AND CAST(SUM(confident_count) AS FLOAT) / CAST(SUM(hit_count) AS FLOAT) >= ?
		ORDER BY SUM(confident_count) DESC
		LIMIT ?`, confidentMin, ratioMin, batchSize)
	if err != nil {
		return 0, fmt.Errorf("study store: scan candidates: %w", err)
	}
	defer rows.Close()

	type pointAgg struct {
		pointID        string
		confidentCount int
		hitCount       int
	}
	var aggs []pointAgg
	for rows.Next() {
		var a pointAgg
		if err := rows.Scan(&a.pointID, &a.confidentCount, &a.hitCount); err != nil {
			return 0, fmt.Errorf("study store: scan row: %w", err)
		}
		aggs = append(aggs, a)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, a := range aggs {
		var representative string
		err := s.db.QueryRow(`
			SELECT question_terms FROM question_kp_cooccurrence
			WHERE point_id = ?
			ORDER BY confident_count DESC, last_seen_at DESC, question_terms
			LIMIT 1`, a.pointID).Scan(&representative)
		if err != nil {
			return count, fmt.Errorf("study store: representative label: %w", err)
		}

		candidateID := uuid.New().String()
		_, err = s.db.Exec(`
			INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(question_terms, point_id) DO UPDATE SET
				confident_count = excluded.confident_count,
				hit_count = excluded.hit_count`,
			candidateID, representative, a.pointID, a.confidentCount, a.hitCount)
		if err != nil {
			return count, fmt.Errorf("study store: upsert candidate: %w", err)
		}
		count++
	}
	return count, nil
}

// CooccurrenceLabelsForPoint returns the distinct subject labels that have at
// least one confident co-occurrence with the point — the inputs for deriving
// an aggregated link's subject condition (交集，见 service.tryCreateLink).
func (s *Store) CooccurrenceLabelsForPoint(pointID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT question_terms FROM question_kp_cooccurrence
		WHERE point_id = ? AND confident_count > 0`, pointID)
	if err != nil {
		return nil, fmt.Errorf("study store: labels for point: %w", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, fmt.Errorf("study store: labels scan: %w", err)
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// ConfidentTraceQuadruple is one confident citation of a point — the unit of
// observed_conditions induction (replaces per-field unions).
type ConfidentTraceQuadruple struct {
	Subject       string
	Intent        string
	Audience      string
	Constraint    string
	QuestionTerms string
	CreatedAt     time.Time
}

// ConfidentTraceQuadruples returns every confident trace that actually cited
// pointID (citation-level, not label-only join — same guard as
// ConfidentTraceFieldValues, 2026-07-21). Used by buildObservedConditions.
func (s *Store) ConfidentTraceQuadruples(pointID string) ([]ConfidentTraceQuadruple, error) {
	rows, err := s.db.Query(`
		SELECT t.subject, t.intent, t.audience, t.constraint_text, t.question_terms, t.created_at
		FROM traces t
		WHERE t.retrieval_quality = 'confident'
		  AND EXISTS (SELECT 1 FROM json_each(t.direct_point_ids) je WHERE je.value = ?)
		ORDER BY t.created_at`, pointID)
	if err != nil {
		return nil, fmt.Errorf("study store: confident trace quadruples: %w", err)
	}
	defer rows.Close()

	var out []ConfidentTraceQuadruple
	for rows.Next() {
		var q ConfidentTraceQuadruple
		if err := rows.Scan(&q.Subject, &q.Intent, &q.Audience, &q.Constraint, &q.QuestionTerms, &q.CreatedAt); err != nil {
			return nil, fmt.Errorf("study store: confident trace quadruples scan: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ConfidentTraceFieldValues returns the distinct raw (un-normalized)
// intent/audience/constraint_text values across every confident trace that
// actually cited pointID. Retained for tests / diagnostics; Match induction
// now uses ConfidentTraceQuadruples instead.
func (s *Store) ConfidentTraceFieldValues(pointID string) (intents, audiences, constraints []string, err error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT t.intent, t.audience, t.constraint_text
		FROM question_kp_cooccurrence c
		JOIN traces t ON t.question_terms = c.question_terms AND t.retrieval_quality = 'confident'
		WHERE c.point_id = ? AND c.confident_count > 0
		  AND EXISTS (SELECT 1 FROM json_each(t.direct_point_ids) je WHERE je.value = c.point_id)`, pointID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("study store: confident trace field values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var in, au, co string
		if err := rows.Scan(&in, &au, &co); err != nil {
			return nil, nil, nil, fmt.Errorf("study store: confident trace field values scan: %w", err)
		}
		intents = append(intents, in)
		audiences = append(audiences, au)
		constraints = append(constraints, co)
	}
	return intents, audiences, constraints, rows.Err()
}

// Step 2: FetchUnprocessedGapEvents returns unprocessed knowledge_gap learning events.
func (s *Store) FetchUnprocessedGapEvents() ([]GapEvent, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id, le.trace_id, le.payload, t.question_terms
		FROM learning_events le
		JOIN traces t ON le.trace_id = t.trace_id
		WHERE le.processed = 0 AND le.event_type = 'knowledge_gap'
		ORDER BY le.created_at`)
	if err != nil {
		return nil, fmt.Errorf("study store: fetch gap events: %w", err)
	}
	defer rows.Close()

	var events []GapEvent
	for rows.Next() {
		var e GapEvent
		var payload string
		if err := rows.Scan(&e.EventID, &e.TraceID, &payload, &e.QuestionTerms); err != nil {
			return nil, fmt.Errorf("study store: scan gap event: %w", err)
		}
		var p map[string]string
		if err := json.Unmarshal([]byte(payload), &p); err == nil {
			e.Question = p["question"]
			e.Reason = p["reason"]
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// UpsertKnowledgeGap implements docs/impl/v1/study.md "knowledge_gaps 表扩展":
// reason_counts is a JSON map incremented per-reason (not a SQL ON CONFLICT
// increment — SQLite here isn't built with the JSON1 functions this repo
// otherwise avoids relying on), so the read-modify-write happens in a
// transaction to stay correct if this is ever called concurrently.
func (s *Store) UpsertKnowledgeGap(questionTerms, question, reason, traceID string) (gapID string, hitCount int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, fmt.Errorf("study store: upsert gap begin tx: %w", err)
	}
	defer tx.Rollback()

	var reasonCountsJSON string
	err = tx.QueryRow(`SELECT gap_id, reason_counts FROM knowledge_gaps WHERE question_terms = ?`, questionTerms).
		Scan(&gapID, &reasonCountsJSON)
	switch {
	case err == sql.ErrNoRows:
		counts := map[string]int{}
		if reason != "" {
			counts[reason] = 1
		}
		countsJSON, _ := json.Marshal(counts)
		gapID = uuid.New().String()
		if _, err = tx.Exec(`
			INSERT INTO knowledge_gaps (gap_id, question_terms, question, hit_count, reason_counts, last_reason, last_trace_id)
			VALUES (?, ?, ?, 1, ?, ?, ?)`,
			gapID, questionTerms, question, string(countsJSON), reason, traceID); err != nil {
			return "", 0, fmt.Errorf("study store: insert gap: %w", err)
		}
		hitCount = 1
	case err != nil:
		return "", 0, fmt.Errorf("study store: get existing gap: %w", err)
	default:
		counts := map[string]int{}
		if reasonCountsJSON != "" {
			_ = json.Unmarshal([]byte(reasonCountsJSON), &counts)
		}
		if reason != "" {
			counts[reason]++
		}
		countsJSON, _ := json.Marshal(counts)
		if _, err = tx.Exec(`
			UPDATE knowledge_gaps SET
				hit_count = hit_count + 1,
				question = ?,
				reason_counts = ?,
				last_reason = ?,
				last_trace_id = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE gap_id = ?`,
			question, string(countsJSON), reason, traceID, gapID); err != nil {
			return "", 0, fmt.Errorf("study store: update gap: %w", err)
		}
		if err = tx.QueryRow(`SELECT hit_count FROM knowledge_gaps WHERE gap_id = ?`, gapID).Scan(&hitCount); err != nil {
			return "", 0, fmt.Errorf("study store: get updated hit_count: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("study store: upsert gap commit: %w", err)
	}
	return gapID, hitCount, nil
}

func (s *Store) MarkEventProcessed(eventID string) error {
	_, err := s.db.Exec(`UPDATE learning_events SET processed = 1 WHERE event_id = ?`, eventID)
	if err != nil {
		return fmt.Errorf("study store: mark processed: %w", err)
	}
	return nil
}

// RawGapEvent is an unprocessed activation_gap learning_events row
// (docs/impl/v1/study.md 步骤 2 来源 B).
type RawGapEvent struct {
	EventID string
	Payload string
}

func (s *Store) FetchUnprocessedActivationGapEvents() ([]RawGapEvent, error) {
	rows, err := s.db.Query(`
		SELECT event_id, payload FROM learning_events
		WHERE processed = 0 AND event_type = 'activation_gap'
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("study store: fetch activation_gap events: %w", err)
	}
	defer rows.Close()

	var events []RawGapEvent
	for rows.Next() {
		var e RawGapEvent
		if err := rows.Scan(&e.EventID, &e.Payload); err != nil {
			return nil, fmt.Errorf("study store: scan activation_gap event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// RawSynonymGapEvent is an unprocessed subject_synonym_gap learning_events
// row joined to its trace's question_hash — the input to
// aggregateSynonymGaps (docs/impl/v1/study.md 步骤 2a,
// docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
type RawSynonymGapEvent struct {
	EventID      string
	Payload      string
	QuestionHash string
}

// FetchUnprocessedSynonymGapEvents returns unprocessed subject_synonym_gap
// learning_events rows.
func (s *Store) FetchUnprocessedSynonymGapEvents() ([]RawSynonymGapEvent, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id, le.payload, COALESCE(t.question_hash, '')
		FROM learning_events le
		LEFT JOIN traces t ON le.trace_id = t.trace_id
		WHERE le.processed = 0 AND le.event_type = 'subject_synonym_gap'
		ORDER BY le.created_at`)
	if err != nil {
		return nil, fmt.Errorf("study store: fetch subject_synonym_gap events: %w", err)
	}
	defer rows.Close()

	var events []RawSynonymGapEvent
	for rows.Next() {
		var e RawSynonymGapEvent
		if err := rows.Scan(&e.EventID, &e.Payload, &e.QuestionHash); err != nil {
			return nil, fmt.Errorf("study store: scan subject_synonym_gap event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ActivationGapEventIDsForPoint returns learning_events.event_id values for
// activation_gap rows whose payload.direct_point_ids contains pointID.
// Used by Source A (cooccurrence) create_candidate so learning_results.event_ids
// stays a list of real learning_event ids (docs/impl/v1/study.md), not
// link_candidates.candidate_id.
func (s *Store) ActivationGapEventIDsForPoint(pointID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id
		FROM learning_events le, json_each(json_extract(le.payload, '$.direct_point_ids')) AS pid
		WHERE le.event_type = 'activation_gap' AND pid.value = ?
		ORDER BY le.created_at`, pointID)
	if err != nil {
		return nil, fmt.Errorf("study store: activation_gap event ids for point: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("study store: scan activation_gap event id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CooccurrenceConfidentCount looks up a single (question_terms, point_id)
// cooccurrence row's confident_count, for source-B candidate qualification
// (docs/impl/v1/study.md 步骤 2).
// CooccurrenceConfidentCount aggregates confident co-occurrence across all
// subject labels for the point (2026-07-18 修订，与 ScanCandidates 同因：标签
// 不稳定会把复现信号打散在多行；来源 B 的"至少再复现一次"看的应是该 KP 的总
// 复现次数，不是恰好同标签的复现次数)。
func (s *Store) CooccurrenceConfidentCount(pointID string) (count int, found bool, err error) {
	var rows int
	err = s.db.QueryRow(`SELECT COALESCE(SUM(confident_count), 0), COUNT(*)
		FROM question_kp_cooccurrence WHERE point_id = ?`, pointID).Scan(&count, &rows)
	if err != nil {
		return 0, false, fmt.Errorf("study store: cooccurrence confident count: %w", err)
	}
	return count, rows > 0, nil
}

// LatestConfidentTraceQuadruple fetches the Session four-tuple from the most
// recent confident trace under questionTerms, for CreateLink's LinkCondition
// (docs/impl/v1/study.md 步骤 2, docs/impl/v1/activation.md 数据结构).
func (s *Store) LatestConfidentTraceQuadruple(questionTerms string) (subject, intent, audience, constraintText string, found bool, err error) {
	err = s.db.QueryRow(`SELECT subject, intent, audience, constraint_text FROM traces
		WHERE question_terms = ? AND retrieval_quality = 'confident'
		ORDER BY created_at DESC LIMIT 1`, questionTerms).Scan(&subject, &intent, &audience, &constraintText)
	if err == sql.ErrNoRows {
		return "", "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", "", false, fmt.Errorf("study store: latest confident trace quadruple: %w", err)
	}
	return subject, intent, audience, constraintText, true, nil
}

// PointLifecycleCurrent reports whether pointID's KP is still lifecycle=current
// (docs/impl/v1/study.md 步骤 3, "目标 KP lifecycle != current 的链接：跳过一切强化").
func (s *Store) PointLifecycleCurrent(pointID string) (bool, error) {
	var lifecycle string
	err := s.db.QueryRow(`SELECT lifecycle FROM knowledge_points WHERE point_id = ?`, pointID).Scan(&lifecycle)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("study store: point lifecycle: %w", err)
	}
	return lifecycle == "current", nil
}

// SignalEventsInWindow returns every activation_success / activation_failure /
// user_correction learning_events row (any processed state) created within
// the trailing windowDays, joined to traces for question_hash — the raw
// input to aggregateSignals (docs/impl/v1/study.md 步骤 3).
func (s *Store) SignalEventsInWindow(windowDays int) ([]RawSignalEvent, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id, le.event_type, le.payload, le.processed, le.created_at, COALESCE(t.question_hash, '')
		FROM learning_events le
		LEFT JOIN traces t ON le.trace_id = t.trace_id
		WHERE le.event_type IN ('activation_success', 'activation_failure', 'user_correction')
		  AND le.created_at >= datetime('now', '-' || ? || ' days')
		ORDER BY le.created_at`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("study store: signal events in window: %w", err)
	}
	defer rows.Close()
	return scanSignalEvents(rows)
}

// UnprocessedSignalEvents returns every unprocessed activation_success /
// activation_failure / user_correction event regardless of age — the batch
// this cycle must consume (docs/impl/v1/study.md 步骤 3).
func (s *Store) UnprocessedSignalEvents() ([]RawSignalEvent, error) {
	rows, err := s.db.Query(`
		SELECT le.event_id, le.event_type, le.payload, le.processed, le.created_at, COALESCE(t.question_hash, '')
		FROM learning_events le
		LEFT JOIN traces t ON le.trace_id = t.trace_id
		WHERE le.processed = 0 AND le.event_type IN ('activation_success', 'activation_failure', 'user_correction')
		ORDER BY le.created_at`)
	if err != nil {
		return nil, fmt.Errorf("study store: unprocessed signal events: %w", err)
	}
	defer rows.Close()
	return scanSignalEvents(rows)
}

func scanSignalEvents(rows *sql.Rows) ([]RawSignalEvent, error) {
	var events []RawSignalEvent
	for rows.Next() {
		var e RawSignalEvent
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Payload, &e.Processed, &e.CreatedAt, &e.QuestionHash); err != nil {
			return nil, fmt.Errorf("study store: scan signal event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// CandidateLinksOlderThan returns candidate links created before the trailing
// idleDays window, for the idle-eviction scan (docs/impl/v1/study.md 步骤 4).
func (s *Store) CandidateLinksOlderThan(idleDays int) ([]string, error) {
	return s.linkIDsWhere(`status = 'candidate' AND created_at < datetime('now', '-' || ? || ' days')`, idleDays)
}

// WeakenedLinksOlderThan returns weakened links whose last transition is
// older than the trailing idleDays window (docs/impl/v1/study.md 步骤 4).
func (s *Store) WeakenedLinksOlderThan(idleDays int) ([]string, error) {
	return s.linkIDsWhere(`status = 'weakened' AND status_changed_at < datetime('now', '-' || ? || ' days')`, idleDays)
}

func (s *Store) linkIDsWhere(whereClause string, arg int) ([]string, error) {
	rows, err := s.db.Query(`SELECT link_id FROM activation_links WHERE `+whereClause, arg)
	if err != nil {
		return nil, fmt.Errorf("study store: link ids where: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("study store: scan link id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasPendingResult reports whether a pending_confirm learning_result already
// exists for (action, objectType, objectID), to avoid re-flagging the same
// candidate every cycle (docs/impl/v1/study.md 步骤 5/6).
func (s *Store) HasPendingResult(action, objectType, objectID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM learning_results
		WHERE action = ? AND object_type = ? AND object_id = ? AND status = 'pending_confirm' LIMIT 1`,
		action, objectType, objectID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("study store: has pending result: %w", err)
	}
	return true, nil
}

// ListLearningResults implements GET /study/results (docs/impl/v1/study.md 步骤 8).
func (s *Store) ListLearningResults(action, objectType, objectID, status string, limit int) ([]LearningResultRow, error) {
	query := `SELECT lr.result_id, lr.action, lr.object_type, lr.object_id, lr.reason, lr.event_ids,
		lr.status, COALESCE(lr.confirmed_by, ''), lr.created_at, lr.updated_at,
		COALESCE(al.question_terms, ''), COALESCE(kp.content, '')
		FROM learning_results lr
		LEFT JOIN activation_links al ON lr.object_type = 'activation_link' AND al.link_id = lr.object_id
		LEFT JOIN knowledge_points kp ON kp.point_id = al.point_id
		WHERE 1 = 1`
	var args []interface{}
	if action != "" {
		query += ` AND lr.action = ?`
		args = append(args, action)
	}
	if objectType != "" {
		query += ` AND lr.object_type = ?`
		args = append(args, objectType)
	}
	if objectID != "" {
		query += ` AND lr.object_id = ?`
		args = append(args, objectID)
	}
	if status != "" {
		query += ` AND lr.status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY lr.created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("study store: list learning results: %w", err)
	}
	defer rows.Close()

	var results []LearningResultRow
	for rows.Next() {
		var r LearningResultRow
		var eventIDsStr string
		if err := rows.Scan(&r.ResultID, &r.Action, &r.ObjectType, &r.ObjectID, &r.Reason, &eventIDsStr,
			&r.Status, &r.ConfirmedBy, &r.CreatedAt, &r.UpdatedAt, &r.QuestionTerms, &r.PointSummary); err != nil {
			return nil, fmt.Errorf("study store: scan learning result: %w", err)
		}
		json.Unmarshal([]byte(eventIDsStr), &r.EventIDs)
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetLearningResult implements GET /study/results/:id (docs/impl/v1/study.md 步骤 8).
func (s *Store) GetLearningResult(resultID string) (*LearningResultDetail, error) {
	var d LearningResultDetail
	var eventIDsStr string
	err := s.db.QueryRow(`SELECT lr.result_id, lr.action, lr.object_type, lr.object_id, lr.reason, lr.event_ids,
		lr.status, COALESCE(lr.confirmed_by, ''), lr.created_at, lr.updated_at,
		COALESCE(al.question_terms, ''), COALESCE(kp.content, '')
		FROM learning_results lr
		LEFT JOIN activation_links al ON lr.object_type = 'activation_link' AND al.link_id = lr.object_id
		LEFT JOIN knowledge_points kp ON kp.point_id = al.point_id
		WHERE lr.result_id = ?`, resultID).
		Scan(&d.ResultID, &d.Action, &d.ObjectType, &d.ObjectID, &d.Reason, &eventIDsStr,
			&d.Status, &d.ConfirmedBy, &d.CreatedAt, &d.UpdatedAt, &d.QuestionTerms, &d.PointSummary)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("study store: get learning result: %w", err)
	}
	json.Unmarshal([]byte(eventIDsStr), &d.EventIDs)

	for _, eventID := range d.EventIDs {
		var ev LearningEventSummary
		err := s.db.QueryRow(`SELECT event_id, event_type, payload, created_at FROM learning_events WHERE event_id = ?`,
			eventID).Scan(&ev.EventID, &ev.EventType, &ev.Payload, &ev.CreatedAt)
		if err == nil {
			d.Events = append(d.Events, ev)
		}
	}
	return &d, nil
}

// Step 3: Report generation queries

func (s *Store) QueryTraceSummary(periodDays int) (*TraceSummary, error) {
	var summary TraceSummary
	rows, err := s.db.Query(`
		SELECT retrieval_quality, COUNT(*)
		FROM traces
		WHERE created_at >= datetime('now', '-' || ? || ' days')
		GROUP BY retrieval_quality`, periodDays)
	if err != nil {
		return nil, fmt.Errorf("study store: trace summary: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var quality string
		var count int
		if err := rows.Scan(&quality, &count); err != nil {
			return nil, fmt.Errorf("study store: scan trace summary: %w", err)
		}
		summary.TotalTraces += count
		switch quality {
		case "confident":
			summary.ConfidentCount = count
		case "partial":
			summary.PartialCount = count
		case "gap":
			summary.GapCount = count
		}
	}
	if summary.TotalTraces > 0 {
		summary.ConfidentRate = float64(summary.ConfidentCount) / float64(summary.TotalTraces)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.db.QueryRow(`SELECT COUNT(*) FROM question_kp_cooccurrence`).Scan(&summary.TotalCooccurrencePairs)
	if err != nil {
		return nil, fmt.Errorf("study store: cooccurrence count: %w", err)
	}

	err = s.db.QueryRow(`SELECT COUNT(*) FROM link_candidates`).Scan(&summary.CandidatesFlagged)
	if err != nil {
		return nil, fmt.Errorf("study store: candidates count: %w", err)
	}

	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(kpn_cited_count), 0), COALESCE(SUM(cited_count), 0)
		FROM traces
		WHERE created_at >= datetime('now', '-' || ? || ' days')`, periodDays).
		Scan(&summary.KPNCitedCount, &summary.CitedCount)
	if err != nil {
		return nil, fmt.Errorf("study store: kpn citation stats: %w", err)
	}
	if summary.CitedCount > 0 {
		summary.KPNCitationRate = float64(summary.KPNCitedCount) / float64(summary.CitedCount)
	}

	var fastCount int
	err = s.db.QueryRow(`
		SELECT COUNT(*) FROM traces
		WHERE path_type = 'fast' AND created_at >= datetime('now', '-' || ? || ' days')`, periodDays).
		Scan(&fastCount)
	if err != nil {
		return nil, fmt.Errorf("study store: fast path count: %w", err)
	}
	if summary.TotalTraces > 0 {
		summary.FastPathRate = float64(fastCount) / float64(summary.TotalTraces)
	}

	return &summary, nil
}

func (s *Store) ListLinkCandidates() ([]LinkCandidateRow, error) {
	rows, err := s.db.Query(`
		SELECT lc.candidate_id, lc.question_terms, lc.point_id, lc.confident_count, lc.hit_count,
			kp.content AS point_summary, ku.center AS unit_topic,
			COALESCE(ku.entry_id, '') AS entry_id, COALESCE(c.name, '') AS entry_name
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		LEFT JOIN entries c ON ku.entry_id = c.entry_id`)
	if err != nil {
		return nil, fmt.Errorf("study store: list candidates: %w", err)
	}
	defer rows.Close()

	var results []LinkCandidateRow
	for rows.Next() {
		var r LinkCandidateRow
		if err := rows.Scan(&r.CandidateID, &r.QuestionTerms, &r.PointID, &r.ConfidentCount, &r.HitCount,
			&r.PointSummary, &r.UnitTopic, &r.EntryID, &r.ConceptName); err != nil {
			return nil, fmt.Errorf("study store: scan candidate: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) ActivationBreadth(pointID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT question_terms) FROM question_kp_cooccurrence
		WHERE point_id = ? AND confident_count > 0`, pointID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("study store: activation breadth: %w", err)
	}
	return count, nil
}

func (s *Store) ConfidentTracesInPeriod(periodDays int) ([]TracePathRow, error) {
	rows, err := s.db.Query(`
		SELECT path, direct_point_ids FROM traces
		WHERE retrieval_quality = 'confident'
		  AND created_at >= datetime('now', '-' || ? || ' days')`, periodDays)
	if err != nil {
		return nil, fmt.Errorf("study store: confident traces: %w", err)
	}
	defer rows.Close()

	var results []TracePathRow
	for rows.Next() {
		var r TracePathRow
		if err := rows.Scan(&r.Path, &r.DirectPointIDsJSON); err != nil {
			return nil, fmt.Errorf("study store: scan trace path: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) HasKPNNeighbors(pointID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) > 0 FROM knowledge_point_relations
		WHERE source_point_id = ? OR target_point_id = ?`, pointID, pointID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("study store: has kpn neighbors: %w", err)
	}
	return count > 0, nil
}

// Wiki candidates

// QualifyingKPsByEntryFromCandidates excludes entries with merged_into
// set — a merged concept is no longer a valid Wiki candidate entry point
// (docs/impl/v1/concept-evolution.md 步骤 4). In practice a merge's own
// transaction already repoints every KU off the merged entry_id, so this
// join is a defensive backstop rather than the primary guarantee.
//
// The verified-ActivationLink requirement implements docs/design/wiki-compilation.md
// "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格立传'":
// reliability is answered once, by the KP's ActivationLink being verified
// (itself already a success-count + distinct-question judgment) — there is
// no separate confident_count floor here, since re-checking a second,
// independently invented count on top would just re-ask the same question
// verified already answered. confident_count is still selected (MAX) for
// QualifyingKP.ConfidentCount, used only for material-ordering downstream
// (docs/impl/v1/wiki.md 步骤 3), not as a filter.
func (s *Store) QualifyingKPsByEntryFromCandidates() (map[string][]QualifyingKP, error) {
	rows, err := s.db.Query(`
		SELECT ku.entry_id, lc.point_id, MAX(lc.confident_count) AS max_confident, kp.content AS point_summary
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN entries c ON ku.entry_id = c.entry_id
		WHERE ku.entry_id IS NOT NULL AND ku.entry_id != '' AND c.merged_into IS NULL
			AND kp.lifecycle = 'current' AND ku.lifecycle = 'current'
			AND EXISTS (SELECT 1 FROM activation_links al WHERE al.point_id = lc.point_id AND al.status = 'verified')
		GROUP BY lc.point_id`)
	if err != nil {
		return nil, fmt.Errorf("study store: qualifying kps: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]QualifyingKP)
	for rows.Next() {
		var conceptID, pointID, pointSummary string
		var confidentCount int
		if err := rows.Scan(&conceptID, &pointID, &confidentCount, &pointSummary); err != nil {
			return nil, fmt.Errorf("study store: scan qualifying kp: %w", err)
		}
		result[conceptID] = append(result[conceptID], QualifyingKP{
			PointID:        pointID,
			PointSummary:   pointSummary,
			ConfidentCount: confidentCount,
		})
	}
	return result, rows.Err()
}

func (s *Store) EntryInfo(conceptID string) (conceptName, domainID string, err error) {
	err = s.db.QueryRow(`SELECT name, domain_id FROM entries WHERE entry_id = ?`, conceptID).
		Scan(&conceptName, &domainID)
	if err != nil {
		return "", "", fmt.Errorf("study store: concept info: %w", err)
	}
	return
}

// PairSignals builds the weighted edge list feeding concept cohesion scoring
// (docs/impl/v1/wiki-generation.md 2.2/2.4, docs/design/wiki-compilation.md
// "连贯性判断还需要第三层"): for every unordered pair within pointIDs that
// has a KPN relation (related or contradicts — both count positive, see
// docs/impl/v1/wiki-generation.md 2.1 "contradicts 计正权": knowledge points
// that contradict each other are still talking about the same thing) or
// shared confident-question co-occurrence, one graph.Edge combining both
// signals. This intentionally covers only the two signals already queried
// elsewhere in this file; the fuller edge model (ActivationLink observed
// intent Jaccard, same-KU bonus) is Wiki's own aspect-clustering input and
// is deferred alongside outline/section generation
// (docs/impl/v1/wiki-generation.md 阶段 B) — cohesion scoring only needs a
// graph meaningfully better than raw connected components, not the
// full-fidelity model.
func (s *Store) PairSignals(pointIDs []string, wRel, wCooc float64, coocSat int) ([]graph.Edge, error) {
	if len(pointIDs) < 2 {
		return nil, nil
	}
	if coocSat <= 0 {
		coocSat = 3
	}

	weights := make(map[[2]string]float64)
	addWeight := func(a, b string, w float64) {
		if a == b {
			return
		}
		k := edgeKey(a, b)
		weights[k] += w
	}

	ph, args := buildPlaceholders(pointIDs)
	allArgs := append(append([]interface{}{}, args...), args...)
	relRows, err := s.db.Query(fmt.Sprintf(`
		SELECT DISTINCT source_point_id, target_point_id FROM knowledge_point_relations
		WHERE source_point_id IN (%s) AND target_point_id IN (%s)`, ph, ph), allArgs...)
	if err != nil {
		return nil, fmt.Errorf("study store: pair signals relations: %w", err)
	}
	for relRows.Next() {
		var a, b string
		if err := relRows.Scan(&a, &b); err != nil {
			relRows.Close()
			return nil, fmt.Errorf("study store: scan pair relation: %w", err)
		}
		addWeight(a, b, wRel)
	}
	if err := relRows.Err(); err != nil {
		relRows.Close()
		return nil, err
	}
	relRows.Close()

	coocPh, coocArgs := buildPlaceholders(pointIDs)
	coocAllArgs := append(append([]interface{}{}, coocArgs...), coocArgs...)
	coocRows, err := s.db.Query(fmt.Sprintf(`
		SELECT a.point_id, b.point_id, COUNT(DISTINCT a.question_terms) AS n
		FROM question_kp_cooccurrence a
		JOIN question_kp_cooccurrence b
		  ON a.question_terms = b.question_terms AND a.point_id < b.point_id
		WHERE a.confident_count > 0 AND b.confident_count > 0
		  AND a.point_id IN (%s) AND b.point_id IN (%s)
		GROUP BY a.point_id, b.point_id`, coocPh, coocPh), coocAllArgs...)
	if err != nil {
		return nil, fmt.Errorf("study store: pair signals cooccurrence: %w", err)
	}
	for coocRows.Next() {
		var a, b string
		var n int
		if err := coocRows.Scan(&a, &b, &n); err != nil {
			coocRows.Close()
			return nil, fmt.Errorf("study store: scan pair cooccurrence: %w", err)
		}
		ratio := float64(n) / float64(coocSat)
		if ratio > 1 {
			ratio = 1
		}
		addWeight(a, b, wCooc*ratio)
	}
	if err := coocRows.Err(); err != nil {
		coocRows.Close()
		return nil, err
	}
	coocRows.Close()

	edges := make([]graph.Edge, 0, len(weights))
	for k, w := range weights {
		edges = append(edges, graph.Edge{A: k[0], B: k[1], Weight: w})
	}
	return edges, nil
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

func edgeKey(a, b string) [2]string {
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// KPNConnectionCountsByType implements docs/design/wiki-compilation.md
// "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格立传'"'s
// 连贯性 gate: connectivity among qualifying KPs must be split by relation
// type, since a count dominated by contradicts relations means the topic
// hasn't settled enough to warrant a single stable write-up, even if the
// raw connection count clears a threshold.
func (s *Store) KPNConnectionCountsByType(pointIDs []string) (related, contradicts int, err error) {
	if len(pointIDs) < 2 {
		return 0, 0, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(pointIDs)*2)
	for i, id := range pointIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	args2 := make([]interface{}, len(args))
	copy(args2, args)
	allArgs := append(args, args2...)

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT relation_type, COUNT(*) FROM knowledge_point_relations
		WHERE source_point_id IN (%s) AND target_point_id IN (%s)
		GROUP BY relation_type`,
		placeholders, placeholders), allArgs...)
	if err != nil {
		return 0, 0, fmt.Errorf("study store: kpn connection counts by type: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var relType string
		var count int
		if err := rows.Scan(&relType, &count); err != nil {
			return 0, 0, fmt.Errorf("study store: scan kpn connection count: %w", err)
		}
		switch relType {
		case "related":
			related = count
		case "contradicts":
			contradicts = count
		}
	}
	return related, contradicts, rows.Err()
}

func (s *Store) DaysActive(pointIDs []string) (int, error) {
	if len(pointIDs) == 0 {
		return 0, nil
	}
	placeholders := ""
	args := make([]interface{}, 0, len(pointIDs))
	for i, id := range pointIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}

	var count int
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(DISTINCT DATE(last_seen_at)) FROM question_kp_cooccurrence
		WHERE point_id IN (%s)`, placeholders), args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("study store: days active: %w", err)
	}
	return count, nil
}

// ListCrossSourceConflicts implements docs/impl/v1/kpn.md 步骤 5: a read-only
// query into the (unit-owned) knowledge_point_relations table for the
// report's cross_source_conflicts section — display only, no automated action.
func (s *Store) ListCrossSourceConflicts(limit int) ([]CrossSourceConflict, error) {
	rows, err := s.db.Query(`
		SELECT r.relation_id,
			kpa.point_id, kpa.content, sa.title,
			kpb.point_id, kpb.content, sb.title,
			r.created_at
		FROM knowledge_point_relations r
		JOIN knowledge_points kpa ON r.source_point_id = kpa.point_id
		JOIN knowledge_points kpb ON r.target_point_id = kpb.point_id
		JOIN sources sa ON kpa.source_id = sa.source_id
		JOIN sources sb ON kpb.source_id = sb.source_id
		WHERE r.relation_type = 'contradicts' AND r.scope = 'cross'
		ORDER BY r.created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("study store: list cross source conflicts: %w", err)
	}
	defer rows.Close()

	var results []CrossSourceConflict
	for rows.Next() {
		var c CrossSourceConflict
		if err := rows.Scan(&c.RelationID,
			&c.PointA.PointID, &c.PointA.Content, &c.PointA.SourceTitle,
			&c.PointB.PointID, &c.PointB.Content, &c.PointB.SourceTitle,
			&c.CreatedAt); err != nil {
			return nil, fmt.Errorf("study store: scan cross source conflict: %w", err)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

func (s *Store) TopKnowledgeGaps(limit int) ([]KnowledgeGapRow, error) {
	rows, err := s.db.Query(`
		SELECT gap_id, question_terms, question, hit_count, reason_counts, last_reason, last_trace_id
		FROM knowledge_gaps
		ORDER BY hit_count DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("study store: top gaps: %w", err)
	}
	defer rows.Close()

	var results []KnowledgeGapRow
	for rows.Next() {
		var r KnowledgeGapRow
		if err := rows.Scan(&r.GapID, &r.QuestionTerms, &r.Question, &r.HitCount,
			&r.ReasonCountsJSON, &r.LastReason, &r.LastTraceID); err != nil {
			return nil, fmt.Errorf("study store: scan gap: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Report persistence

func (s *Store) SaveReport(reportID string, periodDays int, content string) error {
	_, err := s.db.Exec(`INSERT INTO study_reports (report_id, period_days, content) VALUES (?, ?, ?)`,
		reportID, periodDays, content)
	if err != nil {
		return fmt.Errorf("study store: save report: %w", err)
	}
	return nil
}

func (s *Store) CleanOldReports(maxKeep int) error {
	_, err := s.db.Exec(`
		DELETE FROM study_reports
		WHERE report_id NOT IN (
			SELECT report_id FROM study_reports ORDER BY created_at DESC LIMIT ?
		)`, maxKeep)
	if err != nil {
		return fmt.Errorf("study store: clean reports: %w", err)
	}
	return nil
}

func (s *Store) ListReports() ([]ReportMeta, error) {
	rows, err := s.db.Query(`
		SELECT report_id, period_days, content, created_at
		FROM study_reports
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("study store: list reports: %w", err)
	}
	defer rows.Close()

	var results []ReportMeta
	for rows.Next() {
		var r ReportMeta
		var content string
		if err := rows.Scan(&r.ReportID, &r.PeriodDays, &content, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("study store: scan report: %w", err)
		}
		var report Report
		if err := json.Unmarshal([]byte(content), &report); err == nil {
			r.CandidatesCount = len(report.ActivationLinkCandidates)
			r.WikiCount = len(report.WikiCandidates)
			r.GapCount = len(report.KnowledgeGaps)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) GetReport(reportID string) (*string, error) {
	var content string
	err := s.db.QueryRow(`SELECT content FROM study_reports WHERE report_id = ?`, reportID).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("study store: get report: %w", err)
	}
	return &content, nil
}

func (s *Store) GetLatestReport() (*string, error) {
	var content string
	err := s.db.QueryRow(`SELECT content FROM study_reports ORDER BY created_at DESC LIMIT 1`).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("study store: get latest report: %w", err)
	}
	return &content, nil
}

// Candidates query for API

func (s *Store) ListCandidatesFiltered(recommendation string, limit int) ([]LinkCandidateRow, error) {
	candidates, err := s.ListLinkCandidates()
	if err != nil {
		return nil, err
	}
	// Filtering by recommendation requires computing stats; we return all and let service filter.
	_ = recommendation
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (s *Store) ListGaps(minHitCount, limit int, reason string) ([]KnowledgeGapRow, error) {
	rows, err := s.db.Query(`
		SELECT gap_id, question_terms, question, hit_count, reason_counts, last_reason, last_trace_id
		FROM knowledge_gaps
		WHERE hit_count >= ? AND (? = '' OR last_reason = ?)
		ORDER BY hit_count DESC
		LIMIT ?`, minHitCount, reason, reason, limit)
	if err != nil {
		return nil, fmt.Errorf("study store: list gaps: %w", err)
	}
	defer rows.Close()

	var results []KnowledgeGapRow
	for rows.Next() {
		var r KnowledgeGapRow
		if err := rows.Scan(&r.GapID, &r.QuestionTerms, &r.Question, &r.HitCount,
			&r.ReasonCountsJSON, &r.LastReason, &r.LastTraceID); err != nil {
			return nil, fmt.Errorf("study store: scan gap: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) CooccurrenceLastSeen(pointID string) (time.Time, error) {
	var lastSeenStr sql.NullString
	err := s.db.QueryRow(`
		SELECT MAX(last_seen_at) FROM question_kp_cooccurrence
		WHERE point_id = ?`, pointID).Scan(&lastSeenStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("study store: last seen: %w", err)
	}
	if !lastSeenStr.Valid || lastSeenStr.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", lastSeenStr.String)
	if err != nil {
		t, err = time.Parse(time.RFC3339, lastSeenStr.String)
	}
	if err != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// RecentCrossRelationPointIDs returns the distinct point_ids (both sides) of
// scope='cross' knowledge_point_relations created after since — the
// incremental input for wiki page relation recompute
// (docs/impl/v1/wiki.md 步骤 7b: "只重算涉及新增 knowledge_point_relations 的
// 页面对，不做全库两两扫描").
func (s *Store) RecentCrossRelationPointIDs(since time.Time) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT source_point_id FROM knowledge_point_relations WHERE scope = 'cross' AND created_at > ?
		UNION
		SELECT target_point_id FROM knowledge_point_relations WHERE scope = 'cross' AND created_at > ?`,
		since, since)
	if err != nil {
		return nil, fmt.Errorf("study store: recent cross relation point ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("study store: scan recent cross relation point id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// WikiDraftReflowStats backs the "wiki_draft_reflow" report item
// (docs/impl/v1/study.md 步骤 6): every origin=wiki_draft source with its
// produced-KP count and skipped self-ancestor edge count.
func (s *Store) WikiDraftReflowStats() ([]WikiDraftReflowRow, error) {
	rows, err := s.db.Query(`
		SELECT s.source_id, s.origin_page_id, s.reflow_skipped_edges,
			(SELECT COUNT(*) FROM knowledge_points kp
			 JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
			 WHERE ku.source_id = s.source_id) AS produced_kp_count
		FROM sources s WHERE s.origin = 'wiki_draft'`)
	if err != nil {
		return nil, fmt.Errorf("study store: wiki draft reflow stats: %w", err)
	}
	defer rows.Close()

	var out []WikiDraftReflowRow
	for rows.Next() {
		var row WikiDraftReflowRow
		var originPageID sql.NullString
		if err := rows.Scan(&row.SourceID, &originPageID, &row.SkippedAncestorEdges, &row.ProducedKPCount); err != nil {
			return nil, fmt.Errorf("study store: scan wiki draft reflow row: %w", err)
		}
		row.OriginPageID = originPageID.String
		out = append(out, row)
	}
	return out, rows.Err()
}

// TopicDecomposeSignals backs the "topic_decompose" report item
// (docs/impl/v1/study.md 步骤 6): every topic_decompose_signal learning_event
// in the window, decoded. Report-only aggregation — never drives a learning
// action.
func (s *Store) TopicDecomposeSignals(windowDays int) ([]TopicDecomposeSignalRow, error) {
	rows, err := s.db.Query(`
		SELECT payload FROM learning_events
		WHERE event_type = 'topic_decompose_signal' AND created_at >= datetime('now', ?)`,
		fmt.Sprintf("-%d days", windowDays))
	if err != nil {
		return nil, fmt.Errorf("study store: topic decompose signals: %w", err)
	}
	defer rows.Close()

	var out []TopicDecomposeSignalRow
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("study store: scan topic decompose signal: %w", err)
		}
		var row TopicDecomposeSignalRow
		if err := json.Unmarshal([]byte(payload), &row); err != nil {
			slog.Warn("study store: decode topic_decompose_signal payload failed", "error", err)
			continue
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ComplexityTraces backs docs/impl/v1/study.md 步骤 7's "问题复杂度观测量":
// every trace in the window with the fields needed to group by four-tuple
// and compute per-group metrics. Grouping itself (and the
// activation.Matcher-consistent normalization) happens in the caller — this
// just returns the raw rows.
func (s *Store) ComplexityTraces(windowDays int) ([]ComplexityTraceRow, error) {
	rows, err := s.db.Query(`
		SELECT trace_id, subject, intent, audience, constraint_text, path_type,
			json_array_length(direct_point_ids), COALESCE(skeleton_page_id, '')
		FROM traces WHERE created_at >= datetime('now', ?)`,
		fmt.Sprintf("-%d days", windowDays))
	if err != nil {
		return nil, fmt.Errorf("study store: complexity traces: %w", err)
	}
	defer rows.Close()

	var out []ComplexityTraceRow
	for rows.Next() {
		var row ComplexityTraceRow
		if err := rows.Scan(&row.TraceID, &row.Subject, &row.Intent, &row.Audience, &row.Constraint,
			&row.PathType, &row.DirectPointCount, &row.SkeletonPageID); err != nil {
			return nil, fmt.Errorf("study store: scan complexity trace: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// TopicClusterTraceRow is one trace's quadruple + question + timestamp,
// consumed by flagTopicPageCandidates' quadruple clustering
// (docs/impl/v1/wiki.md 步骤 8, 2026-08-03 修订 "四元组聚类"). Deliberately
// does NOT filter to retrieval_quality='confident' or require non-empty
// direct_point_ids — unlike ConfidentTraceQuadruples/ComplexityTraces, topic
// candidate identification answers "有没有人反复问", not "答没答上、引用了
// 哪些知识点", so it consumes every trace in the window.
type TopicClusterTraceRow struct {
	Subject    string
	Intent     string
	Audience   string
	Constraint string
	Question   string
	CreatedAt  time.Time
}

// TopicClusterTraces implements docs/impl/v1/wiki.md 步骤 8 第 1 步.
func (s *Store) TopicClusterTraces(windowDays int) ([]TopicClusterTraceRow, error) {
	rows, err := s.db.Query(`
		SELECT subject, intent, audience, constraint_text, question, created_at
		FROM traces WHERE created_at >= datetime('now', ?)`,
		fmt.Sprintf("-%d days", windowDays))
	if err != nil {
		return nil, fmt.Errorf("study store: topic cluster traces: %w", err)
	}
	defer rows.Close()

	var out []TopicClusterTraceRow
	for rows.Next() {
		var r TopicClusterTraceRow
		if err := rows.Scan(&r.Subject, &r.Intent, &r.Audience, &r.Constraint, &r.Question, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("study store: scan topic cluster trace: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasNonRejectedTopicCandidate implements docs/impl/v1/wiki.md 步骤 8's
// "去重": a topic_page_candidate learning_result whose reason carries this
// quadruple's fingerprint and isn't status='rejected' means this quadruple
// already has a live candidate (pending_confirm or applied) — don't produce
// another one for the same real-use signal.
func (s *Store) HasNonRejectedTopicCandidate(fingerprint string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM learning_results
		WHERE action = 'topic_page_candidate' AND status != 'rejected'
			AND reason LIKE ? LIMIT 1`,
		"[topic_fp:"+fingerprint+"]%").Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("study store: has non-rejected topic candidate: %w", err)
	}
	return true, nil
}

type WikiDraftReflowRow struct {
	SourceID             string
	OriginPageID         string
	ProducedKPCount      int
	SkippedAncestorEdges int
}

// TopicDecomposeSignalRow mirrors the combined topic_decompose_signal
// payload shape (docs/impl/v1/trace.md's event payload).
type TopicDecomposeSignalRow struct {
	PageID                string   `json:"page_id"`
	MemberPageIDs         []string `json:"member_page_ids"`
	ResolvedMemberPageIDs []string `json:"resolved_member_page_ids"`
	ResolvedOutsideCount  int      `json:"resolved_outside_count"`
	Unresolved            bool     `json:"unresolved"`
}

type ComplexityTraceRow struct {
	TraceID          string
	Subject          string
	Intent           string
	Audience         string
	Constraint       string
	PathType         string
	DirectPointCount int
	SkeletonPageID   string
}

func init() {
	_ = slog.Debug
}
