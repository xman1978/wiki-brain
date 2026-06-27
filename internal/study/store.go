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

func (s *Store) UpsertKnowledgeGap(questionTerms, question string) (int, error) {
	gapID := uuid.New().String()
	var hitCount int
	_, err := s.db.Exec(`
		INSERT INTO knowledge_gaps (gap_id, question_terms, question)
		VALUES (?, ?, ?)
		ON CONFLICT(question_terms) DO UPDATE SET
			hit_count = hit_count + 1,
			question = excluded.question,
			updated_at = CURRENT_TIMESTAMP`,
		gapID, questionTerms, question)
	if err != nil {
		return 0, fmt.Errorf("study store: upsert gap: %w", err)
	}
	err = s.db.QueryRow(`SELECT hit_count FROM knowledge_gaps WHERE question_terms = ?`, questionTerms).Scan(&hitCount)
	if err != nil {
		return 0, fmt.Errorf("study store: get gap hit_count: %w", err)
	}
	return hitCount, nil
}

func (s *Store) MarkEventProcessed(eventID string) error {
	_, err := s.db.Exec(`UPDATE learning_events SET processed = 1 WHERE event_id = ?`, eventID)
	if err != nil {
		return fmt.Errorf("study store: mark processed: %w", err)
	}
	return nil
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

func (s *Store) QualifyingKPsByConceptFromCandidates(wikiConfidentMin int) (map[string][]QualifyingKP, error) {
	rows, err := s.db.Query(`
		SELECT ku.concept_id, lc.point_id, lc.confident_count
		FROM link_candidates lc
		JOIN knowledge_points kp ON lc.point_id = kp.point_id
		JOIN knowledge_units ku ON kp.unit_id = ku.unit_id
		WHERE lc.confident_count >= ? AND ku.concept_id IS NOT NULL AND ku.concept_id != ''`,
		wikiConfidentMin)
	if err != nil {
		return nil, fmt.Errorf("study store: qualifying kps: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]QualifyingKP)
	for rows.Next() {
		var conceptID, pointID string
		var confidentCount int
		if err := rows.Scan(&conceptID, &pointID, &confidentCount); err != nil {
			return nil, fmt.Errorf("study store: scan qualifying kp: %w", err)
		}
		result[conceptID] = append(result[conceptID], QualifyingKP{
			PointID:        pointID,
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
