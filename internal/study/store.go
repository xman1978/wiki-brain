package study

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Step 1: ScanCandidates queries cooccurrence rows meeting thresholds and upserts link_candidates.
func (s *Store) ScanCandidates(confidentMin int, ratioMin float64, batchSize int) (int, error) {
	rows, err := s.db.Query(`
		SELECT question_terms, point_id, confident_count, hit_count
		FROM question_kp_cooccurrence
		WHERE confident_count >= ?
		  AND CAST(confident_count AS FLOAT) / CAST(hit_count AS FLOAT) >= ?
		ORDER BY confident_count DESC
		LIMIT ?`, confidentMin, ratioMin, batchSize)
	if err != nil {
		return 0, fmt.Errorf("study store: scan candidates: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var questionTerms, pointID string
		var confidentCount, hitCount int
		if err := rows.Scan(&questionTerms, &pointID, &confidentCount, &hitCount); err != nil {
			return count, fmt.Errorf("study store: scan row: %w", err)
		}

		candidateID := uuid.New().String()
		_, err := s.db.Exec(`
			INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(question_terms, point_id) DO UPDATE SET
				confident_count = excluded.confident_count,
				hit_count = excluded.hit_count`,
			candidateID, questionTerms, pointID, confidentCount, hitCount)
		if err != nil {
			return count, fmt.Errorf("study store: upsert candidate: %w", err)
		}
		count++
	}
	return count, rows.Err()
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
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) UpsertKnowledgeGap(questionTerms, question string) (gapID string, hitCount int, err error) {
	newID := uuid.New().String()
	_, err = s.db.Exec(`
		INSERT INTO knowledge_gaps (gap_id, question_terms, question)
		VALUES (?, ?, ?)
		ON CONFLICT(question_terms) DO UPDATE SET
			hit_count = hit_count + 1,
			question = excluded.question,
			updated_at = CURRENT_TIMESTAMP`,
		newID, questionTerms, question)
	if err != nil {
		return "", 0, fmt.Errorf("study store: upsert gap: %w", err)
	}
	// ON CONFLICT keeps the row's original gap_id (only hit_count/question/updated_at
	// are updated), so re-query rather than trust newID.
	err = s.db.QueryRow(`SELECT gap_id, hit_count FROM knowledge_gaps WHERE question_terms = ?`, questionTerms).
		Scan(&gapID, &hitCount)
	if err != nil {
		return "", 0, fmt.Errorf("study store: get gap hit_count: %w", err)
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

// CooccurrenceConfidentCount looks up a single (question_terms, point_id)
// cooccurrence row's confident_count, for source-B candidate qualification
// (docs/impl/v1/study.md 步骤 2).
func (s *Store) CooccurrenceConfidentCount(questionTerms, pointID string) (count int, found bool, err error) {
	err = s.db.QueryRow(`SELECT confident_count FROM question_kp_cooccurrence
		WHERE question_terms = ? AND point_id = ?`, questionTerms, pointID).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("study store: cooccurrence confident count: %w", err)
	}
	return count, true, nil
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
			COALESCE(ku.concept_id, '') AS concept_id, COALESCE(c.name, '') AS concept_name
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		LEFT JOIN concepts c ON ku.concept_id = c.concept_id`)
	if err != nil {
		return nil, fmt.Errorf("study store: list candidates: %w", err)
	}
	defer rows.Close()

	var results []LinkCandidateRow
	for rows.Next() {
		var r LinkCandidateRow
		if err := rows.Scan(&r.CandidateID, &r.QuestionTerms, &r.PointID, &r.ConfidentCount, &r.HitCount,
			&r.PointSummary, &r.UnitTopic, &r.ConceptID, &r.ConceptName); err != nil {
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

// QualifyingKPsByConceptFromCandidates excludes concepts with merged_into
// set — a merged concept is no longer a valid Wiki candidate entry point
// (docs/impl/v1/concept-evolution.md 步骤 4). In practice a merge's own
// transaction already repoints every KU off the merged concept_id, so this
// join is a defensive backstop rather than the primary guarantee.
func (s *Store) QualifyingKPsByConceptFromCandidates(wikiConfidentMin int) (map[string][]QualifyingKP, error) {
	rows, err := s.db.Query(`
		SELECT ku.concept_id, lc.point_id, lc.confident_count, kp.content AS point_summary
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		JOIN concepts c ON ku.concept_id = c.concept_id
		WHERE lc.confident_count >= ? AND ku.concept_id IS NOT NULL AND ku.concept_id != '' AND c.merged_into IS NULL`,
		wikiConfidentMin)
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

func (s *Store) ConceptInfo(conceptID string) (conceptName, domainID string, err error) {
	err = s.db.QueryRow(`SELECT name, domain_id FROM concepts WHERE concept_id = ?`, conceptID).
		Scan(&conceptName, &domainID)
	if err != nil {
		return "", "", fmt.Errorf("study store: concept info: %w", err)
	}
	return
}

func (s *Store) KPNConnectionCount(pointIDs []string) (int, error) {
	if len(pointIDs) < 2 {
		return 0, nil
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

	var count int
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM knowledge_point_relations
		WHERE source_point_id IN (%s) AND target_point_id IN (%s)`,
		placeholders, placeholders), allArgs...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("study store: kpn connection count: %w", err)
	}
	return count, nil
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
		SELECT gap_id, question_terms, question, hit_count
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
		if err := rows.Scan(&r.GapID, &r.QuestionTerms, &r.Question, &r.HitCount); err != nil {
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

func (s *Store) ListGaps(minHitCount, limit int) ([]KnowledgeGapRow, error) {
	rows, err := s.db.Query(`
		SELECT gap_id, question_terms, question, hit_count
		FROM knowledge_gaps
		WHERE hit_count >= ?
		ORDER BY hit_count DESC
		LIMIT ?`, minHitCount, limit)
	if err != nil {
		return nil, fmt.Errorf("study store: list gaps: %w", err)
	}
	defer rows.Close()

	var results []KnowledgeGapRow
	for rows.Next() {
		var r KnowledgeGapRow
		if err := rows.Scan(&r.GapID, &r.QuestionTerms, &r.Question, &r.HitCount); err != nil {
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

func init() {
	_ = slog.Debug
}
