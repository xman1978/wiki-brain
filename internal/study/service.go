package study

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/concept"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

type Service struct {
	store             *Store
	cfg               config.StudyConfig
	activationSvc     *activation.Service
	wikiSvc           *wiki.Service
	recompileNewKPMin int
	conceptSvc        *concept.Service
}

func NewService(store *Store, cfg config.StudyConfig, activationSvc *activation.Service, wikiSvc *wiki.Service, recompileNewKPMin int) *Service {
	return &Service{store: store, cfg: cfg, activationSvc: activationSvc, wikiSvc: wikiSvc, recompileNewKPMin: recompileNewKPMin}
}

// SetConceptSvc wires the (optional) concept-candidate scan appended to this
// Ticker's task chain (docs/impl/v1/concept-evolution.md 步骤 2, run after
// study.md's own step 6 and before report generation). Run still works
// without it (the scan step just no-ops).
func (s *Service) SetConceptSvc(c *concept.Service) {
	s.conceptSvc = c
}

// Run executes a full study cycle (docs/impl/v1/study.md 实现步骤): scan →
// create candidates → link signal judgment (incl. promotion confirm flow) →
// idle eviction → gap aggregation/wiki flagging → report generation. Steps
// 2-6 are new in V1 and log-and-continue on error rather than aborting the
// cycle, per "单步异常记录 error 日志，不中断本轮后续步骤"; steps 1/7 keep
// MVP's abort-on-error behavior unchanged.
func (s *Service) Run() (*RunResult, error) {
	start := time.Now()

	candidatesFlagged, err := s.store.ScanCandidates(
		s.cfg.CandidateConfidentMin, s.cfg.CandidateRatioMin, s.cfg.ScanBatchSize)
	if err != nil {
		return nil, fmt.Errorf("study: scan candidates: %w", err)
	}

	var actions LearningActionsSummary

	if err := s.createCandidates(&actions); err != nil {
		slog.Error("study: create candidates failed", "error", err)
	}

	if err := s.processLinkSignals(&actions); err != nil {
		slog.Error("study: process link signals failed", "error", err)
	}

	if err := s.evictIdle(&actions); err != nil {
		slog.Error("study: evict idle links failed", "error", err)
	}

	gapEventsProcessed, err := s.aggregateGaps()
	if err != nil {
		return nil, fmt.Errorf("study: aggregate gaps: %w", err)
	}

	if err := s.flagWikiCandidates(); err != nil {
		slog.Error("study: flag wiki candidates failed", "error", err)
	}

	if err := s.flagWikiRecompile(); err != nil {
		slog.Error("study: flag wiki recompile failed", "error", err)
	}

	var conceptScan concept.ScanSummary
	if s.conceptSvc != nil {
		conceptScan = s.conceptSvc.Scan()
	}

	report, err := s.generateReport(actions, conceptScan)
	if err != nil {
		return nil, fmt.Errorf("study: generate report: %w", err)
	}

	return &RunResult{
		ReportID:           report.ReportID,
		CandidatesFlagged:  candidatesFlagged,
		GapEventsProcessed: gapEventsProcessed,
		LearningActions:    actions,
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

		gapID, hitCount, err := s.store.UpsertKnowledgeGap(e.QuestionTerms, e.Question, e.Reason, e.TraceID)
		if err != nil {
			return processed, err
		}

		if hitCount == s.cfg.GapHitThreshold {
			slog.Warn("study: knowledge gap reached threshold",
				"question_terms", e.QuestionTerms,
				"hit_count", hitCount)
			reason := fmt.Sprintf("命中次数达到阈值 hit_count=%d", hitCount)
			lr := &activation.LearningResult{
				Action:     activation.ActionGapFlag,
				ObjectType: activation.ObjectTypeKnowledgeGap,
				ObjectID:   gapID,
				Reason:     reason,
				EventIDs:   marshalIDs([]string{e.EventID}),
				Status:     activation.ResultApplied,
			}
			if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
				slog.Error("study: insert gap_flag learning result failed", "gap_id", gapID, "error", err)
			}
		}

		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *Service) generateReport(actions LearningActionsSummary, conceptScan concept.ScanSummary) (*Report, error) {
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

	conflicts, err := s.store.ListCrossSourceConflicts(20)
	if err != nil {
		return nil, err
	}

	conceptCandidates, err := s.buildConceptCandidatesSection(conceptScan)
	if err != nil {
		return nil, err
	}

	report := &Report{
		ReportID:                 reportID,
		GeneratedAt:              time.Now().UTC(),
		PeriodDays:               periodDays,
		Summary:                  *summary,
		ActivationLinkCandidates: activationCandidates,
		WikiCandidates:           wikiCandidates,
		KnowledgeGaps:            gaps,
		LearningActions:          actions,
		CrossSourceConflicts:     conflicts,
		ConceptCandidates:        conceptCandidates,
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

func marshalIDs(ids []string) string {
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
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
			QuestionTerms: r.QuestionTerms,
			PointID:       r.PointID,
			PointSummary:  r.PointSummary,
			UnitTopic:     r.UnitTopic,
			ConceptID:     r.ConceptID,
			ConceptName:   r.ConceptName,
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
		qualifyingPoints := make([]WikiQualifyingPoint, len(kps))
		totalConfident := 0
		for i, kp := range kps {
			pointIDs[i] = kp.PointID
			qualifyingPoints[i] = WikiQualifyingPoint{
				PointID:        kp.PointID,
				PointSummary:   kp.PointSummary,
				ConfidentCount: kp.ConfidentCount,
			}
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
			QualifyingPoints:   qualifyingPoints,
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

// buildConceptCandidatesSection folds this cycle's concept.Scan() counts and
// the currently pending add/merge candidates into the report
// (docs/impl/v1/concept-evolution.md 步骤 5). No-op (zero section) when
// conceptSvc isn't wired.
func (s *Service) buildConceptCandidatesSection(scan concept.ScanSummary) (ConceptCandidatesSection, error) {
	section := ConceptCandidatesSection{
		AddCreated:           scan.AddCreated,
		AddUpdated:           scan.AddUpdated,
		MergeCreated:         scan.MergeCreated,
		MergeUpdated:         scan.MergeUpdated,
		Expired:              scan.Expired,
		ConceptGapEventCount: scan.ConceptGapEventCount,
	}
	if s.conceptSvc == nil {
		return section, nil
	}

	pendingAdd, err := s.conceptSvc.ListCandidateViews(concept.StatusPendingConfirm)
	if err != nil {
		return section, err
	}
	for _, c := range pendingAdd {
		if c.Kind == concept.KindAdd {
			section.PendingAdd = append(section.PendingAdd, c)
		} else if c.Kind == concept.KindMerge {
			section.PendingMerge = append(section.PendingMerge, c)
		}
	}
	return section, nil
}

func (s *Service) buildGapEntries() ([]KnowledgeGapEntry, error) {
	gaps, err := s.store.TopKnowledgeGaps(20)
	if err != nil {
		return nil, err
	}

	entries := make([]KnowledgeGapEntry, len(gaps))
	for i, g := range gaps {
		entries[i] = gapEntryFromRow(g)
	}
	return entries, nil
}

// gapEntryFromRow implements docs/impl/v1/study.md 步骤 7's recommendation
// breakdown by last_reason — no_candidates means the pipeline never found a
// candidate (真缺材料), judge_filtered means candidates existed but rerank
// judged them all irrelevant (指向 semantics-curation.md 的人工修正流程,
// 不是缺材料), answer_error means evidence existed but generation failed
// (系统异常, 不是知识缺口). Empty/unrecognized reason (pre-migration rows,
// or the "unspecified" fallback) keeps the historical default.
func gapEntryFromRow(g KnowledgeGapRow) KnowledgeGapEntry {
	counts := map[string]int{}
	if g.ReasonCountsJSON != "" {
		_ = json.Unmarshal([]byte(g.ReasonCountsJSON), &counts)
	}

	recommendation := "补充材料"
	switch g.LastReason {
	case retrieval.GapReasonJudgeFiltered:
		recommendation = "语义提取待核对"
	case "answer_error":
		recommendation = "生成异常，需查日志"
	}

	return KnowledgeGapEntry{
		QuestionTerms:  g.QuestionTerms,
		Question:       g.Question,
		HitCount:       g.HitCount,
		ReasonCounts:   counts,
		LastReason:     g.LastReason,
		LastTraceID:    g.LastTraceID,
		Recommendation: recommendation,
	}
}

// flagWikiCandidates writes a pending_confirm wiki_candidate learning_result
// for every "ready" Wiki candidate that doesn't already have one pending —
// Wiki compilation itself stays a human-triggered action (docs/impl/v1/study.md
// 步骤 6, docs/impl/v1/wiki.md 步骤 2).
func (s *Service) flagWikiCandidates() error {
	candidates, err := s.buildWikiCandidates()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if c.Recommendation != "ready" {
			continue
		}
		pending, err := s.store.HasPendingResult(activation.ActionWikiCandidate, activation.ObjectTypeWikiPage, c.ConceptID)
		if err != nil {
			slog.Error("study: check pending wiki_candidate failed", "concept_id", c.ConceptID, "error", err)
			continue
		}
		if pending {
			continue
		}
		lr := &activation.LearningResult{
			Action:     activation.ActionWikiCandidate,
			ObjectType: activation.ObjectTypeWikiPage,
			ObjectID:   c.ConceptID,
			Reason:     c.Reason,
			EventIDs:   marshalIDs(c.QualifyingPointIDs),
			Status:     activation.ResultPendingConfirm,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert wiki_candidate learning result failed", "concept_id", c.ConceptID, "error", err)
		}
	}
	return nil
}

// flagWikiRecompile implements docs/impl/v1/study.md 步骤 6 piece b
// (docs/impl/v1/wiki.md 步骤 5b): published pages whose concept gained
// qualifying KPs (same threshold/query as the Wiki candidate scan above)
// since compile time get marked needs_recompile via the Wiki module, and the
// action is recorded for audit. No-op until main.go wires a wikiSvc.
func (s *Service) flagWikiRecompile() error {
	if s.wikiSvc == nil {
		return nil
	}

	qualifyingByConcept, err := s.store.QualifyingKPsByConceptFromCandidates(s.cfg.WikiConfidentMin)
	if err != nil {
		return err
	}
	counts := make(map[string]int, len(qualifyingByConcept))
	for conceptID, kps := range qualifyingByConcept {
		counts[conceptID] = len(kps)
	}

	flagged, err := s.wikiSvc.ScanForNewQualifyingKP(counts, s.recompileNewKPMin)
	if err != nil {
		return err
	}
	for _, f := range flagged {
		lr := &activation.LearningResult{
			Action:     activation.ActionRecompileFlag,
			ObjectType: activation.ObjectTypeWikiPage,
			ObjectID:   f.PageID,
			Reason:     f.Reason,
			Status:     activation.ResultApplied,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert recompile_flag learning result failed", "page_id", f.PageID, "error", err)
		}
	}
	return nil
}

// createCandidates implements docs/impl/v1/study.md 步骤 2: two sources,
// merged and deduped by simply checking "does a link already exist for this
// (question_terms, point_id)" before creating — whichever source reaches a
// pair first wins, and the other source (or a later cycle) becomes a no-op.
func (s *Service) createCandidates(actions *LearningActionsSummary) error {
	// Source A: link_candidates rows already meet ScanCandidates' threshold
	// by construction (its own WHERE clause), so every row here qualifies.
	candidates, err := s.store.ListLinkCandidates()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		ratio := 0.0
		if c.HitCount > 0 {
			ratio = float64(c.ConfidentCount) / float64(c.HitCount)
		}
		reason := fmt.Sprintf("共现命中：confident_count=%d, hit_count=%d, ratio=%.2f", c.ConfidentCount, c.HitCount, ratio)
		created, err := s.tryCreateLink(c.QuestionTerms, c.PointID, []string{c.CandidateID}, reason)
		if err != nil {
			slog.Error("study: create candidate (cooccurrence) failed",
				"question_terms", c.QuestionTerms, "point_id", c.PointID, "error", err)
			continue
		}
		if created {
			actions.CreatedCandidates++
		}
	}

	// Source B: activation_gap events — a gap event's own confident hit plus
	// at least one more recorded confident occurrence in cooccurrence avoids
	// creating a candidate from a single question/answer pair.
	gapEvents, err := s.store.FetchUnprocessedActivationGapEvents()
	if err != nil {
		return err
	}
	for _, e := range gapEvents {
		var payload struct {
			QuestionTerms  string   `json:"question_terms"`
			DirectPointIDs []string `json:"direct_point_ids"`
		}
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			slog.Warn("study: activation_gap payload malformed", "event_id", e.EventID, "error", err)
		} else {
			for _, pid := range payload.DirectPointIDs {
				count, found, err := s.store.CooccurrenceConfidentCount(pid)
				if err != nil {
					slog.Error("study: cooccurrence lookup failed",
						"question_terms", payload.QuestionTerms, "point_id", pid, "error", err)
					continue
				}
				if !found || count < 2 {
					continue
				}
				reason := fmt.Sprintf("activation_gap 复现：confident_count=%d", count)
				created, err := s.tryCreateLink(payload.QuestionTerms, pid, []string{e.EventID}, reason)
				if err != nil {
					slog.Error("study: create candidate (activation_gap) failed",
						"question_terms", payload.QuestionTerms, "point_id", pid, "error", err)
					continue
				}
				if created {
					actions.CreatedCandidates++
				}
			}
		}
		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			slog.Error("study: mark activation_gap processed failed", "event_id", e.EventID, "error", err)
		}
	}

	return nil
}

// tryCreateLink creates an ActivationLink candidate for the point unless one
// already exists for it — dedup is at point level, any status (2026-07-18 修订：
// 候选达标改为 point 级聚合后，代表标签可能随周期漂移，仅按 (question_terms,
// point_id) 精确去重会给同一 KP 反复创建仅标签不同的新链接)。当该 point 已有
// 链接时，不创建第二条，而是用同一套归纳算子刷新它的条件（computeLinkCondition）
// ——条件不是创建时定死的，会随着更多确证信号持续收敛/扩展；deprecated 是终态，
// 不再刷新。刷新不算新建、不计入 actions.CreatedCandidates，也不写
// learning_results（条件收敛是持续性维护动作，不是一次性学习事件）。
func (s *Service) tryCreateLink(questionTerms, pointID string, createdFrom []string, reason string) (bool, error) {
	existingForPoint, err := s.activationSvc.Store().ListLinks(activation.ListLinksFilter{PointID: pointID, Limit: 1})
	if err != nil {
		return false, err
	}
	if len(existingForPoint) > 0 {
		existing := existingForPoint[0]
		if existing.Status == activation.StatusDeprecated {
			return false, nil
		}
		cond, err := s.computeLinkCondition(pointID, existing.QuestionTerms)
		if err != nil || cond == nil {
			return false, err
		}
		if conditionEqual(cond, &existing.ActivationLink) {
			return false, nil
		}
		return false, s.activationSvc.Store().UpdateConditions(existing.LinkID, *cond)
	}

	cond, err := s.computeLinkCondition(pointID, questionTerms)
	if err != nil || cond == nil {
		return false, err
	}

	link, err := s.activationSvc.CreateLink(questionTerms, *cond, pointID, createdFrom)
	if err != nil {
		slog.Info("study: create_candidate skipped", "question_terms", questionTerms, "point_id", pointID, "reason", err.Error())
		return false, nil
	}

	lr := &activation.LearningResult{
		Action:     activation.ActionCreateCandidate,
		ObjectType: activation.ObjectTypeActivationLink,
		ObjectID:   link.LinkID,
		Reason:     reason,
		EventIDs:   marshalIDs(createdFrom),
		Status:     activation.ResultApplied,
	}
	if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
		return false, err
	}
	return true, nil
}

// computeLinkCondition is the single place that turns a point's accumulated
// confident-trace signal into an activation.LinkCondition — shared by both
// creation (tryCreateLink, new point) and refresh (tryCreateLink, point
// already linked), so the two paths can never drift into different
// normalization rules. Returns (nil, nil) when the point has no confident
// signal at all (nothing to derive a condition from).
//
// subject_terms uses the cross-phrasing label intersection (LabelTermIntersection,
// unchanged logic) when ≥2 labels share ≥2 terms; otherwise it falls back to
// fallbackQuestionTerms' own most recent confident trace's subject — on
// creation that's the triggering question_terms, on refresh it's the link's
// own stored question_terms (a stand-in label, not a matching key anymore).
// intent_terms/audience/constraint_terms are the normalized, deduplicated,
// sorted set of every value observed across *all* of the point's confident
// traces (ConfidentTraceFieldValues) — not just the latest one.
func (s *Service) computeLinkCondition(pointID, fallbackQuestionTerms string) (*activation.LinkCondition, error) {
	labels, err := s.store.CooccurrenceLabelsForPoint(pointID)
	if err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, nil
	}

	subjectTerms := LabelTermIntersection(labels)
	if subjectTerms == "" {
		subject, _, _, _, found, err := s.store.LatestConfidentTraceQuadruple(fallbackQuestionTerms)
		if err != nil {
			return nil, err
		}
		if found {
			subjectTerms = text.Terms(text.Normalize(subject))
		}
	}

	rawIntents, rawAudiences, rawConstraints, err := s.store.ConfidentTraceFieldValues(pointID)
	if err != nil {
		return nil, err
	}

	return &activation.LinkCondition{
		SubjectTerms: subjectTerms,
		IntentTerms: normalizeDedupSorted(rawIntents, func(v string) string {
			return text.Terms(text.Normalize(v))
		}),
		Audience: normalizeDedupSorted(rawAudiences, text.NormalizeCompact),
		ConstraintTerms: normalizeDedupSorted(rawConstraints, func(v string) string {
			return text.Terms(text.Normalize(v))
		}),
	}, nil
}

// normalizeDedupSorted normalizes every value with norm, deduplicates, and
// returns the sorted result — the shape computeLinkCondition needs for each
// of the three accumulated sets (store.go's encodeTermSet sorts again on
// write, but sorting here too keeps conditionEqual's comparison simple).
func normalizeDedupSorted(values []string, norm func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		seen[norm(v)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// conditionEqual reports whether cond matches link's currently stored
// condition — computeLinkCondition already returns sorted slices, so plain
// slice equality suffices (no need to re-sort or use a set comparison).
func conditionEqual(cond *activation.LinkCondition, link *activation.ActivationLink) bool {
	return cond.SubjectTerms == link.SubjectTerms &&
		slicesEqual(cond.IntentTerms, link.IntentTerms) &&
		slicesEqual(cond.Audience, link.Audience) &&
		slicesEqual(cond.ConstraintTerms, link.ConstraintTerms)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// labelTermIntersection derives the cross-phrasing stable core of a point's
// confident subject labels: the terms every label shares. Returns "" (caller
// keeps the trace-quadruple subject) unless there are ≥2 labels to intersect
// and the core keeps ≥2 terms — a 1-term core like "数据库" would make the
// link's subject condition trivially matchable, which the score threshold
// alone shouldn't be trusted to contain.
func LabelTermIntersection(labels []string) string {
	if len(labels) < 2 {
		return ""
	}
	core := text.TermSet(labels[0])
	for _, l := range labels[1:] {
		next := text.TermSet(l)
		for w := range core {
			if _, ok := next[w]; !ok {
				delete(core, w)
			}
		}
		if len(core) == 0 {
			return ""
		}
	}
	if len(core) < 2 {
		return ""
	}
	terms := make([]string, 0, len(core))
	for w := range core {
		terms = append(terms, w)
	}
	sort.Strings(terms)
	return strings.Join(terms, " ")
}

// aggregateSignals groups a slice of activation_success / activation_failure
// / user_correction events by the activation_link_id(s) they reference
// (docs/impl/v1/study.md 步骤 3). Applied to the unprocessed batch it yields
// this cycle's stat deltas; applied to the full event_window_days window it
// yields the cumulative counts used for threshold judgment.
func aggregateSignals(events []RawSignalEvent, correctionWeight int) map[string]*linkSignal {
	result := make(map[string]*linkSignal)
	get := func(id string) *linkSignal {
		a, ok := result[id]
		if !ok {
			a = &linkSignal{distinctHashes: make(map[string]bool)}
			result[id] = a
		}
		return a
	}

	for _, ev := range events {
		switch ev.EventType {
		case "activation_success":
			var p struct {
				LinkID string `json:"link_id"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.LinkID == "" {
				continue
			}
			a := get(p.LinkID)
			a.SuccessN++
			a.EventIDs = append(a.EventIDs, ev.EventID)
			if ev.QuestionHash != "" && !a.distinctHashes[ev.QuestionHash] {
				a.distinctHashes[ev.QuestionHash] = true
				a.DistinctN++
			}
		case "activation_failure":
			var p struct {
				LinkID string `json:"link_id"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.LinkID == "" {
				continue
			}
			a := get(p.LinkID)
			a.FailureN++
			a.EventIDs = append(a.EventIDs, ev.EventID)
		case "user_correction":
			var p struct {
				LinkIDs []string `json:"link_ids"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) != nil {
				continue
			}
			for _, lid := range p.LinkIDs {
				if lid == "" {
					continue
				}
				a := get(lid)
				a.FailureN += correctionWeight
				a.EventIDs = append(a.EventIDs, ev.EventID)
			}
		}
	}
	return result
}

// processLinkSignals implements docs/impl/v1/study.md 步骤 3 (+ 步骤 5 inline
// for the candidate branch): consume the unprocessed signal event batch,
// judge each touched link's state against the event_window_days cumulative
// counts, and update running stats.
func (s *Service) processLinkSignals(actions *LearningActionsSummary) error {
	unprocessed, err := s.store.UnprocessedSignalEvents()
	if err != nil {
		return err
	}
	if len(unprocessed) == 0 {
		return nil
	}

	batch := aggregateSignals(unprocessed, s.cfg.CorrectionWeight)

	windowed, err := s.store.SignalEventsInWindow(s.cfg.EventWindowDays)
	if err != nil {
		return err
	}
	window := aggregateSignals(windowed, s.cfg.CorrectionWeight)

	for linkID, delta := range batch {
		w, ok := window[linkID]
		if !ok {
			w = &linkSignal{}
		}

		link, err := s.activationSvc.GetLink(linkID)
		if err != nil {
			slog.Error("study: get link failed", "link_id", linkID, "error", err)
			continue
		}
		if link == nil {
			continue
		}

		lifecycleCurrent, err := s.store.PointLifecycleCurrent(link.PointID)
		if err != nil {
			slog.Error("study: point lifecycle lookup failed", "link_id", linkID, "point_id", link.PointID, "error", err)
			continue
		}

		// "目标 KP lifecycle != current 的链接：跳过一切强化...不累加 adopt_count，
		// 但失败与降权判定照常" — suppress only the adopt-count increment.
		adoptDelta := delta.SuccessN
		if !lifecycleCurrent {
			adoptDelta = 0
		}
		if err := s.activationSvc.UpdateStats(linkID, adoptDelta, delta.FailureN); err != nil {
			slog.Error("study: update stats failed", "link_id", linkID, "error", err)
		}

		switch link.Status {
		case activation.StatusCandidate:
			if lifecycleCurrent && w.SuccessN >= s.cfg.PromoteSuccessMin && w.DistinctN >= s.cfg.PromoteDistinctMin {
				s.judgePromotion(link, w, actions)
			}
		case activation.StatusVerified:
			total := w.SuccessN + w.FailureN
			if total > 0 && w.FailureN >= s.cfg.WeakenFailureMin && float64(w.FailureN)/float64(total) >= s.cfg.WeakenRatioMin {
				reason := fmt.Sprintf("窗口内 failure_n=%d, success_n=%d, 比值=%.2f",
					w.FailureN, w.SuccessN, float64(w.FailureN)/float64(total))
				if _, err := s.activationSvc.TransitionLink(linkID, activation.StatusWeakened, reason, w.EventIDs); err != nil {
					slog.Error("study: weaken failed", "link_id", linkID, "error", err)
				} else {
					actions.Weakened++
					actions.Actions = append(actions.Actions, LearningActionEntry{
						Action: activation.ActionWeaken, ObjectID: linkID, Reason: reason, Status: activation.ResultApplied,
					})
				}
			}
		case activation.StatusWeakened:
			if lifecycleCurrent && w.SuccessN >= s.cfg.ReverifySuccessMin && w.FailureN == 0 {
				reason := fmt.Sprintf("窗口内 success_n=%d，无 failure", w.SuccessN)
				if _, err := s.activationSvc.TransitionLink(linkID, activation.StatusVerified, reason, w.EventIDs); err != nil {
					slog.Error("study: reverify failed", "link_id", linkID, "error", err)
				} else {
					actions.Reverified++
					actions.Actions = append(actions.Actions, LearningActionEntry{
						Action: activation.ActionReverify, ObjectID: linkID, Reason: reason, Status: activation.ResultApplied,
					})
				}
			}
		}
	}

	for _, e := range unprocessed {
		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			slog.Error("study: mark signal event processed failed", "event_id", e.EventID, "error", err)
		}
	}
	return nil
}

// judgePromotion implements docs/impl/v1/study.md 步骤 5: auto_promote=true
// transitions immediately; otherwise it records a pending_confirm promote
// result (without touching link status) unless one is already pending.
func (s *Service) judgePromotion(link *activation.ActivationLink, w *linkSignal, actions *LearningActionsSummary) {
	reason := fmt.Sprintf("窗口内 success_n=%d，%d 个不同问题", w.SuccessN, w.DistinctN)

	if s.cfg.AutoPromote {
		if _, err := s.activationSvc.TransitionLink(link.LinkID, activation.StatusVerified, reason, w.EventIDs); err != nil {
			slog.Error("study: auto-promote failed", "link_id", link.LinkID, "error", err)
			return
		}
		actions.Promoted++
		actions.Actions = append(actions.Actions, LearningActionEntry{
			Action: activation.ActionPromote, ObjectID: link.LinkID, Reason: reason, Status: activation.ResultApplied,
		})
		return
	}

	pending, err := s.activationSvc.Store().FindPendingPromote(link.LinkID)
	if err != nil {
		slog.Error("study: find pending promote failed", "link_id", link.LinkID, "error", err)
		return
	}
	if pending != nil {
		return
	}

	lr := &activation.LearningResult{
		Action:     activation.ActionPromote,
		ObjectType: activation.ObjectTypeActivationLink,
		ObjectID:   link.LinkID,
		Reason:     reason,
		EventIDs:   marshalIDs(w.EventIDs),
		Status:     activation.ResultPendingConfirm,
	}
	if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
		slog.Error("study: insert pending promote failed", "link_id", link.LinkID, "error", err)
		return
	}
	actions.PendingPromotions++
	actions.Actions = append(actions.Actions, LearningActionEntry{
		Action: activation.ActionPromote, ObjectID: link.LinkID, Reason: reason, Status: activation.ResultPendingConfirm,
	})
}

// evictIdle implements docs/impl/v1/study.md 步骤 4.
func (s *Service) evictIdle(actions *LearningActionsSummary) error {
	if err := s.evictIdleCandidates(actions); err != nil {
		return err
	}
	return s.evictIdleWeakened(actions)
}

func (s *Service) evictIdleCandidates(actions *LearningActionsSummary) error {
	idle, err := s.store.CandidateLinksOlderThan(s.cfg.CandidateIdleDays)
	if err != nil {
		return err
	}
	if len(idle) == 0 {
		return nil
	}

	recent, err := s.store.SignalEventsInWindow(s.cfg.CandidateIdleDays)
	if err != nil {
		return err
	}
	touched := aggregateSignals(recent, 0)

	for _, linkID := range idle {
		if _, ok := touched[linkID]; ok {
			continue
		}
		if _, err := s.activationSvc.TransitionLink(linkID, activation.StatusDeprecated, "idle_candidate", nil); err != nil {
			slog.Error("study: evict idle candidate failed", "link_id", linkID, "error", err)
			continue
		}
		actions.Deprecated++
		actions.Actions = append(actions.Actions, LearningActionEntry{
			Action: activation.ActionDeprecate, ObjectID: linkID, Reason: "idle_candidate", Status: activation.ResultApplied,
		})
	}
	return nil
}

func (s *Service) evictIdleWeakened(actions *LearningActionsSummary) error {
	idle, err := s.store.WeakenedLinksOlderThan(s.cfg.DeprecateIdleDays)
	if err != nil {
		return err
	}
	if len(idle) == 0 {
		return nil
	}

	recent, err := s.store.SignalEventsInWindow(s.cfg.DeprecateIdleDays)
	if err != nil {
		return err
	}
	recentAgg := aggregateSignals(recent, 0)

	for _, linkID := range idle {
		if a, ok := recentAgg[linkID]; ok && a.SuccessN > 0 {
			continue
		}
		if _, err := s.activationSvc.TransitionLink(linkID, activation.StatusDeprecated, "idle_weakened", nil); err != nil {
			slog.Error("study: evict idle weakened failed", "link_id", linkID, "error", err)
			continue
		}
		actions.Deprecated++
		actions.Actions = append(actions.Actions, LearningActionEntry{
			Action: activation.ActionDeprecate, ObjectID: linkID, Reason: "idle_weakened", Status: activation.ResultApplied,
		})
	}
	return nil
}
