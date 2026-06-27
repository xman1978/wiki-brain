package study

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

type Service struct {
	store *Store
	cfg   config.StudyConfig
}

func NewService(store *Store, cfg config.StudyConfig) *Service {
	return &Service{store: store, cfg: cfg}
}

// Run executes a full study cycle: scan → gap aggregation → report generation.
func (s *Service) Run() (*RunResult, error) {
	start := time.Now()

	candidatesFlagged, err := s.store.ScanCandidates(
		s.cfg.CandidateConfidentMin, s.cfg.CandidateRatioMin, s.cfg.ScanBatchSize)
	if err != nil {
		return nil, fmt.Errorf("study: scan candidates: %w", err)
	}

	gapEventsProcessed, err := s.aggregateGaps()
	if err != nil {
		return nil, fmt.Errorf("study: aggregate gaps: %w", err)
	}

	report, err := s.generateReport()
	if err != nil {
		return nil, fmt.Errorf("study: generate report: %w", err)
	}

	return &RunResult{
		ReportID:           report.ReportID,
		CandidatesFlagged:  candidatesFlagged,
		GapEventsProcessed: gapEventsProcessed,
		ElapsedMs:          time.Since(start).Milliseconds(),
	}, nil
}

func (s *Service) aggregateGaps() (int, error) {
	events, err := s.store.FetchUnprocessedGapEvents()
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, e := range events {
		if e.QuestionTerms == "" {
			slog.Warn("study: gap event missing question_terms, skipping", "event_id", e.EventID)
			if err := s.store.MarkEventProcessed(e.EventID); err != nil {
				return processed, err
			}
			processed++
			continue
		}

		hitCount, err := s.store.UpsertKnowledgeGap(e.QuestionTerms, e.Question)
		if err != nil {
			return processed, err
		}

		if hitCount == s.cfg.GapHitThreshold {
			slog.Warn("study: knowledge gap reached threshold",
				"question_terms", e.QuestionTerms,
				"hit_count", hitCount)
		}

		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *Service) generateReport() (*Report, error) {
	reportID := uuid.New().String()
	periodDays := s.cfg.ReportPeriodDays

	summary, err := s.store.QueryTraceSummary(periodDays)
	if err != nil {
		return nil, err
	}

	activationCandidates, err := s.buildActivationCandidates(periodDays)
	if err != nil {
		return nil, err
	}

	wikiCandidates, err := s.buildWikiCandidates()
	if err != nil {
		return nil, err
	}

	gaps, err := s.buildGapEntries()
	if err != nil {
		return nil, err
	}

	report := &Report{
		ReportID:                 reportID,
		GeneratedAt:             time.Now().UTC(),
		PeriodDays:              periodDays,
		Summary:                 *summary,
		ActivationLinkCandidates: activationCandidates,
		WikiCandidates:          wikiCandidates,
		KnowledgeGaps:           gaps,
	}

	content, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("study: marshal report: %w", err)
	}

	if err := s.store.SaveReport(reportID, periodDays, string(content)); err != nil {
		return nil, err
	}

	if err := s.store.CleanOldReports(s.cfg.ReportMaxKeep); err != nil {
		slog.Error("study: clean old reports failed", "error", err)
	}

	return report, nil
}

func (s *Service) buildActivationCandidates(periodDays int) ([]ActivationLinkCandidate, error) {
	rows, err := s.store.ListLinkCandidates()
	if err != nil {
		return nil, err
	}

	traces, err := s.store.ConfidentTracesInPeriod(periodDays)
	if err != nil {
		return nil, err
	}

	var results []ActivationLinkCandidate
	for _, r := range rows {
		signalPurity := 0.0
		if r.HitCount > 0 {
			signalPurity = float64(r.ConfidentCount) / float64(r.HitCount)
		}

		breadth, err := s.store.ActivationBreadth(r.PointID)
		if err != nil {
			return nil, err
		}

		shortPathRate := calcShortPathRate(r.PointID, traces)

		hasNeighbors, err := s.store.HasKPNNeighbors(r.PointID)
		if err != nil {
			return nil, err
		}

		lastSeen, _ := s.store.CooccurrenceLastSeen(r.PointID)

		recommendation := "candidate"
		if signalPurity >= 0.7 && breadth >= 3 && shortPathRate >= 0.6 {
			recommendation = "strong"
		}

		reason := fmt.Sprintf("信号纯度 %.2f，激活广度 %d，短路径占比 %.2f", signalPurity, breadth, shortPathRate)

		results = append(results, ActivationLinkCandidate{
			QuestionTerms:  r.QuestionTerms,
			PointID:        r.PointID,
			PointSummary:   r.PointSummary,
			UnitTopic:      r.UnitTopic,
			ConceptID:      r.ConceptID,
			ConceptName:    r.ConceptName,
			Stats: ActivationLinkStats{
				ConfidentCount:    r.ConfidentCount,
				HitCount:          r.HitCount,
				SignalPurity:      signalPurity,
				ActivationBreadth: breadth,
				ShortPathRate:     shortPathRate,
				HasKPNNeighbors:   hasNeighbors,
				LastSeenAt:        lastSeen,
			},
			Recommendation: recommendation,
			Reason:         reason,
		})
	}

	return results, nil
}

func calcShortPathRate(pointID string, traces []TracePathRow) float64 {
	total := 0
	shortCount := 0
	for _, t := range traces {
		var pointIDs []string
		if err := json.Unmarshal([]byte(t.DirectPointIDsJSON), &pointIDs); err != nil {
			continue
		}
		found := false
		for _, pid := range pointIDs {
			if pid == pointID {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		total++
		if t.Path == "short" {
			shortCount++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(shortCount) / float64(total)
}

func (s *Service) buildWikiCandidates() ([]WikiCandidate, error) {
	qualifyingByConceptMap, err := s.store.QualifyingKPsByConceptFromCandidates(s.cfg.WikiConfidentMin)
	if err != nil {
		return nil, err
	}

	var results []WikiCandidate
	for conceptID, kps := range qualifyingByConceptMap {
		conceptName, domainID, err := s.store.ConceptInfo(conceptID)
		if err != nil {
			slog.Warn("study: concept info not found", "concept_id", conceptID, "error", err)
			continue
		}

		pointIDs := make([]string, len(kps))
		totalConfident := 0
		for i, kp := range kps {
			pointIDs[i] = kp.PointID
			totalConfident += kp.ConfidentCount
		}

		avgConfident := 0.0
		if len(kps) > 0 {
			avgConfident = float64(totalConfident) / float64(len(kps))
		}

		connCount, err := s.store.KPNConnectionCount(pointIDs)
		if err != nil {
			return nil, err
		}

		daysActive, err := s.store.DaysActive(pointIDs)
		if err != nil {
			return nil, err
		}

		recommendation := "needs_more_data"
		if len(kps) >= s.cfg.WikiKPMin && connCount >= 1 {
			recommendation = "ready"
		}

		reason := fmt.Sprintf("%d 个 KP 达到 Wiki 阈值，KPN 连接 %d 条，活跃天数 %d 天",
			len(kps), connCount, daysActive)

		results = append(results, WikiCandidate{
			ConceptID:          conceptID,
			ConceptName:        conceptName,
			DomainID:           domainID,
			QualifyingPointIDs: pointIDs,
			Stats: WikiCandidateStats{
				QualifyingKPCount:  len(kps),
				AvgConfidentCount:  avgConfident,
				KPNConnectionCount: connCount,
				DaysActive:         daysActive,
			},
			Recommendation: recommendation,
			Reason:         reason,
		})
	}
	return results, nil
}

func (s *Service) buildGapEntries() ([]KnowledgeGapEntry, error) {
	gaps, err := s.store.TopKnowledgeGaps(20)
	if err != nil {
		return nil, err
	}

	entries := make([]KnowledgeGapEntry, len(gaps))
	for i, g := range gaps {
		entries[i] = KnowledgeGapEntry{
			QuestionTerms:  g.QuestionTerms,
			Question:       g.Question,
			HitCount:       g.HitCount,
			Recommendation: "补充材料",
		}
	}
	return entries, nil
}
