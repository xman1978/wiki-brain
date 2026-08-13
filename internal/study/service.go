package study

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/entry"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/graph"
	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

// CohesionConfig bundles the concept-cohesion gate's tunables
// (docs/impl/v1/wiki-generation.md 2.2/2.4, docs/design/wiki-compilation.md
// "连贯性判断还需要第三层") — passed as one struct rather than five more
// positional ints/floats on NewService. Zero value (Min<=0) leaves the gate
// inert: Stats.Cohesion is still computed and reported, but it never turns
// an otherwise-ready candidate into a split signal or blocks recommendation
// — this is what every existing test's `CohesionConfig{}` call gets, so
// pre-existing "ready" expectations built on synthetic data that doesn't
// happen to be densely connected are unaffected. Production wiring
// (cmd/server/main.go) passes real wiki.* config values, turning the gate
// on.
type CohesionConfig struct {
	Min     float64
	WRel    float64
	WCooc   float64
	CoocSat int
	Gamma   float64
}

type Service struct {
	store                   *Store
	cfg                     config.StudyConfig
	activationSvc           *activation.Service
	wikiSvc                 *wiki.Service
	recompileNewKPMin       int
	qualifyingMinDaysActive int
	// topicClusterMinQuestions/topicClusterMinDaysActive are
	// wiki.topic_cluster_min_questions/topic_cluster_min_days_active
	// (docs/impl/v1/wiki.md 步骤 8) — owned by wiki.Config but consumed here
	// (flagTopicPageCandidates), same precedent as recompileNewKPMin/
	// qualifyingMinDaysActive above.
	topicClusterMinQuestions  int
	topicClusterMinDaysActive int
	cohesion                  CohesionConfig
	entrySvc                  *entry.Service
	// questionTupleNormIdleDays is retrieval.question_tuple_norm_idle_days
	// (docs/impl/v1/retrieval.md 步骤 2) — owned by RetrievalConfig but
	// consumed here (evictIdle's question_tuple_norms cleanup), same
	// precedent as recompileNewKPMin/topicClusterMinQuestions above: this
	// table isn't owned by Study, but Study is this codebase's existing
	// periodic-housekeeping owner (docs/impl/v1/study.md 步骤 4 idle 清理惯例).
	questionTupleNormIdleDays int
	// lastRelationScanAt is the in-process watermark for
	// recomputePageRelations (docs/impl/v1/wiki.md 步骤 7b) — zero value on
	// first Run scans the whole history once.
	lastRelationScanAt time.Time
}

func NewService(store *Store, cfg config.StudyConfig, activationSvc *activation.Service, wikiSvc *wiki.Service, recompileNewKPMin, qualifyingMinDaysActive int, cohesion CohesionConfig, topicClusterMinQuestions, topicClusterMinDaysActive, questionTupleNormIdleDays int) *Service {
	return &Service{
		store:                     store,
		cfg:                       cfg,
		activationSvc:             activationSvc,
		wikiSvc:                   wikiSvc,
		recompileNewKPMin:         recompileNewKPMin,
		qualifyingMinDaysActive:   qualifyingMinDaysActive,
		cohesion:                  cohesion,
		topicClusterMinQuestions:  topicClusterMinQuestions,
		topicClusterMinDaysActive: topicClusterMinDaysActive,
		questionTupleNormIdleDays: questionTupleNormIdleDays,
	}
}

// SetEntrySvc wires the (optional) concept-candidate scan appended to this
// Ticker's task chain (docs/impl/v1/concept-evolution.md 步骤 2, run after
// study.md's own step 6 and before report generation). Run still works
// without it (the scan step just no-ops).
func (s *Service) SetEntrySvc(c *entry.Service) {
	s.entrySvc = c
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
		s.cfg.CreateConfidenceMin, s.cfg.CreateWidthMax, s.cfg.ScanBatchSize)
	if err != nil {
		return nil, fmt.Errorf("study: scan candidates: %w", err)
	}

	var actions LearningActionsSummary

	if err := s.createCandidates(&actions); err != nil {
		slog.Error("study: create candidates failed", "error", err)
	}

	if err := s.aggregateAndCreateSynonymCandidates(&actions); err != nil {
		slog.Error("study: aggregate subject_synonym_gap failed", "error", err)
	}

	if err := s.evictIdle(&actions); err != nil {
		slog.Error("study: evict idle links failed", "error", err)
	}

	if err := s.pruneConditions(&actions); err != nil {
		slog.Error("study: prune conditions failed", "error", err)
	}

	if err := s.scanActivationBundles(); err != nil {
		slog.Error("study: scan activation bundles failed", "error", err)
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

	if err := s.recomputePageRelations(); err != nil {
		slog.Error("study: recompute page relations failed", "error", err)
	}

	underfilled, err := s.flagTopicPageCandidates(&actions)
	if err != nil {
		slog.Error("study: flag topic page candidates failed", "error", err)
	}

	var entryScan entry.ScanSummary
	if s.entrySvc != nil {
		entryScan = s.entrySvc.Scan()
	}

	report, err := s.generateReport(actions, entryScan, underfilled)
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

func (s *Service) generateReport(actions LearningActionsSummary, entryScan entry.ScanSummary, underfilled []wiki.TopicSignalUnderfilled) (*Report, error) {
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

	wikiCandidates, conceptSplitSignals, err := s.buildWikiCandidatesWithSplitSignals()
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

	conceptCandidates, err := s.buildEntryCandidatesSection(entryScan)
	if err != nil {
		return nil, err
	}

	underfilledEntries := make([]TopicSignalUnderfilledEntry, 0, len(underfilled))
	for _, u := range underfilled {
		underfilledEntries = append(underfilledEntries, TopicSignalUnderfilledEntry{
			Subject: u.Subject, Intent: u.Intent, Audience: u.Audience, ConstraintText: u.ConstraintText,
			DistinctQuestionCount: u.DistinctQuestionCount, DaysActive: u.DaysActive,
		})
	}

	wikiDraftReflow, err := s.buildWikiDraftReflowSection()
	if err != nil {
		slog.Error("study: build wiki_draft_reflow report section failed", "error", err)
	}

	topicDecompose, err := s.buildTopicDecomposeSection(periodDays)
	if err != nil {
		slog.Error("study: build topic_decompose report section failed", "error", err)
	}

	questionComplexity, err := s.buildQuestionComplexitySection(periodDays)
	if err != nil {
		slog.Error("study: build question_complexity report section failed", "error", err)
	}

	convergence, err := s.buildConvergenceSection()
	if err != nil {
		slog.Error("study: build convergence report section failed", "error", err)
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
		EntryCandidates:          conceptCandidates,
		TopicSignalUnderfilled:   underfilledEntries,
		WikiDraftReflow:          wikiDraftReflow,
		TopicDecompose:           topicDecompose,
		QuestionComplexity:       questionComplexity,
		EntrySplitSignals:        conceptSplitSignals,
		Convergence:              convergence,
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
			EntryID:       r.EntryID,
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
	candidates, _, err := s.buildWikiCandidatesWithSplitSignals()
	return candidates, err
}

// buildWikiCandidatesWithSplitSignals is buildWikiCandidates plus the
// cohesion gate's report-only byproduct (docs/impl/v1/wiki-generation.md
// 2.4, docs/design/wiki-compilation.md "连贯性判断还需要第三层"): entries
// that clear every other ready criterion but whose qualifying KPs split into
// several unrelated Louvain communities get a EntrySplitSignalEntry
// instead of (not in addition to) a "ready" recommendation.
func (s *Service) buildWikiCandidatesWithSplitSignals() ([]WikiCandidate, []EntrySplitSignalEntry, error) {
	qualifyingByConceptMap, err := s.store.QualifyingKPsByEntryFromCandidates()
	if err != nil {
		return nil, nil, err
	}

	var results []WikiCandidate
	var splitSignals []EntrySplitSignalEntry
	for conceptID, kps := range qualifyingByConceptMap {
		conceptName, domainID, err := s.store.EntryInfo(conceptID)
		if err != nil {
			slog.Warn("study: concept info not found", "entry_id", conceptID, "error", err)
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

		related, contradicts, err := s.store.KPNConnectionCountsByType(pointIDs)
		if err != nil {
			return nil, nil, err
		}

		daysActive, err := s.store.DaysActive(pointIDs)
		if err != nil {
			return nil, nil, err
		}

		// cohesion (docs/design/wiki-compilation.md "连贯性判断还需要第三层",
		// docs/impl/v1/wiki-generation.md 2.2/2.4): the largest Louvain
		// community's share of qualifying KPs. Always computed (it's cheap
		// and informational on its own via Stats.Cohesion); only gates
		// recommendation when s.cohesion.Min > 0 is configured — see
		// CohesionConfig's doc comment for why the zero value must stay
		// inert.
		cohesion := 1.0
		var communities [][]string
		if len(pointIDs) > 0 {
			edges, perr := s.store.PairSignals(pointIDs, s.cohesion.WRel, s.cohesion.WCooc, s.cohesion.CoocSat)
			if perr != nil {
				slog.Warn("study: pair signals for cohesion failed, treating concept as fully cohesive", "entry_id", conceptID, "error", perr)
			} else {
				communities = graph.Communities(pointIDs, edges, s.cohesion.Gamma)
				cohesion = graph.LargestShare(communities)
			}
		}

		// docs/design/wiki-compilation.md "ActivationLink 回答'这条管不管用'，
		// Wiki 编译回答'这个主题够不够格立传'": 广度（qualifying_kp_count）、
		// 连贯（related 连接存在、contradicts 不反客为主、且这批 KP 是否围绕
		// 一个共同中心而非几个互不相干的簇）、稳定（daysActive 衡量的是跨
		// 时间跨度的持久性，不是问询频率）三者同时满足才 ready；可靠性已经
		// 由 qualifying KP 定义里的 verified 状态单独回答，这里不再重复检查。
		breadthOK := len(kps) >= s.cfg.WikiKPMin
		relatedOK := related >= 1 && contradicts < related
		stableOK := daysActive >= s.qualifyingMinDaysActive
		cohesionOK := s.cohesion.Min <= 0 || cohesion >= s.cohesion.Min

		recommendation := "needs_more_data"
		reason := fmt.Sprintf("%d 个 KP 达到 Wiki 阈值，KPN 连接 related=%d/contradicts=%d，活跃天数 %d 天，内聚度 %.2f",
			len(kps), related, contradicts, daysActive, cohesion)
		if breadthOK && relatedOK && stableOK && cohesionOK {
			recommendation = "ready"
		} else if breadthOK && relatedOK && stableOK && !cohesionOK {
			// Every other gate cleared — this isn't "needs more data", it's
			// "this concept's qualifying material may not be one topic".
			var entryCommunities []EntrySplitCommunity
			for _, c := range communities {
				entryCommunities = append(entryCommunities, EntrySplitCommunity{
					PointIDs:      c,
					SuggestedName: suggestAspectName(c, kps),
				})
			}
			splitSignals = append(splitSignals, EntrySplitSignalEntry{
				EntryID:     conceptID,
				ConceptName: conceptName,
				Cohesion:    cohesion,
				AspectCount: len(communities),
				Communities: entryCommunities,
			})
			reason = fmt.Sprintf("%s（内聚度 %.2f 低于门槛 %.2f，material 疑似分裂为 %d 个互不相干的簇，见 entry_split_signals）",
				reason, cohesion, s.cohesion.Min, len(communities))
		}

		results = append(results, WikiCandidate{
			EntryID:            conceptID,
			ConceptName:        conceptName,
			DomainID:           domainID,
			QualifyingPointIDs: pointIDs,
			QualifyingPoints:   qualifyingPoints,
			Stats: WikiCandidateStats{
				QualifyingKPCount:          len(kps),
				AvgConfidentCount:          avgConfident,
				KPNConnectionCount:         related + contradicts,
				RelatedConnectionCount:     related,
				ContradictsConnectionCount: contradicts,
				DaysActive:                 daysActive,
				Cohesion:                   cohesion,
			},
			Recommendation: recommendation,
			Reason:         reason,
		})
	}
	return results, splitSignals, nil
}

// suggestAspectName gives a Louvain community a display label for
// EntrySplitSignalEntry — the highest-confident_count KP's summary in that
// community, truncated. Not the full aspect-naming scheme of
// docs/impl/v1/wiki-generation.md 2.3 (which also factors in ActivationLink
// intent and is part of the deferred outline-generation rewrite); this is
// just enough for a human skimming the report to recognize which cluster is
// which.
func suggestAspectName(pointIDs []string, kps []QualifyingKP) string {
	bySummary := make(map[string]string, len(kps))
	byConfidence := make(map[string]int, len(kps))
	for _, kp := range kps {
		bySummary[kp.PointID] = kp.PointSummary
		byConfidence[kp.PointID] = kp.ConfidentCount
	}
	best := ""
	bestScore := -1
	for _, pid := range pointIDs {
		if byConfidence[pid] > bestScore {
			bestScore = byConfidence[pid]
			best = bySummary[pid]
		}
	}
	if len(best) > 24 {
		runes := []rune(best)
		if len(runes) > 24 {
			best = string(runes[:24]) + "…"
		}
	}
	return best
}

// buildEntryCandidatesSection folds this cycle's concept.Scan() counts and
// the currently pending add/merge candidates into the report
// (docs/impl/v1/concept-evolution.md 步骤 5). No-op (zero section) when
// entrySvc isn't wired.
func (s *Service) buildEntryCandidatesSection(scan entry.ScanSummary) (EntryCandidatesSection, error) {
	section := EntryCandidatesSection{
		AddCreated:         scan.AddCreated,
		AddUpdated:         scan.AddUpdated,
		MergeCreated:       scan.MergeCreated,
		MergeUpdated:       scan.MergeUpdated,
		Expired:            scan.Expired,
		EntryGapEventCount: scan.EntryGapEventCount,
	}
	if s.entrySvc == nil {
		return section, nil
	}

	pendingAdd, err := s.entrySvc.ListCandidateViews(entry.StatusPendingConfirm)
	if err != nil {
		return section, err
	}
	for _, c := range pendingAdd {
		if c.Kind == entry.KindAdd {
			section.PendingAdd = append(section.PendingAdd, c)
		} else if c.Kind == entry.KindMerge {
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
		pending, err := s.store.HasPendingResult(activation.ActionWikiCandidate, activation.ObjectTypeWikiPage, c.EntryID)
		if err != nil {
			slog.Error("study: check pending wiki_candidate failed", "entry_id", c.EntryID, "error", err)
			continue
		}
		if pending {
			continue
		}
		lr := &activation.LearningResult{
			Action:     activation.ActionWikiCandidate,
			ObjectType: activation.ObjectTypeWikiPage,
			ObjectID:   c.EntryID,
			Reason:     c.Reason,
			EventIDs:   marshalIDs(c.QualifyingPointIDs),
			Status:     activation.ResultPendingConfirm,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert wiki_candidate learning result failed", "entry_id", c.EntryID, "error", err)
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

	qualifyingByEntry, err := s.store.QualifyingKPsByEntryFromCandidates()
	if err != nil {
		return err
	}
	counts := make(map[string]int, len(qualifyingByEntry))
	for conceptID, kps := range qualifyingByEntry {
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

// flagTopicPageCandidates implements docs/impl/v1/study.md 步骤 6's two-tier
// extension (2026-08-03 修订: 四元组聚类替代连通分量,
// docs/impl/v1/wiki.md 步骤 8): group the window's traces by normalized
// (subject,intent,audience,constraint_text), keep groups clearing the
// stable-cluster gate (distinct_question_count/days_active) and not already
// covered by a non-rejected topic_page_candidate, then delegate the rest of
// 步骤 8 (candidate-range retrieval -> qualifying filter -> concept grouping ->
// admission -> shell creation) to the Wiki module per surviving group. Groups
// whose candidate range had zero qualifying KP come back as
// TopicSignalUnderfilled and are passed through to generateReport
// ("topic_signal_underfilled") rather than recomputed there, since
// DetectTopicCandidate has a side effect (shell page creation) that must
// only run once per cycle.
func (s *Service) flagTopicPageCandidates(actions *LearningActionsSummary) ([]wiki.TopicSignalUnderfilled, error) {
	if s.wikiSvc == nil || s.activationSvc == nil {
		return nil, nil
	}

	rows, err := s.store.TopicClusterTraces(s.cfg.EventWindowDays)
	if err != nil {
		return nil, fmt.Errorf("study: topic cluster traces: %w", err)
	}

	type quadKey struct{ subject, intent, audience, constraint string }
	type quadAgg struct {
		questions map[string]bool
		dates     map[string]bool
	}
	groups := make(map[quadKey]*quadAgg)
	var order []quadKey
	for _, r := range rows {
		k := quadKey{r.Subject, r.Intent, r.Audience, r.Constraint}
		a, ok := groups[k]
		if !ok {
			a = &quadAgg{questions: map[string]bool{}, dates: map[string]bool{}}
			groups[k] = a
			order = append(order, k)
		}
		a.questions[r.Question] = true
		a.dates[r.CreatedAt.UTC().Format("2006-01-02")] = true
	}

	minQuestions := s.topicClusterMinQuestions
	if minQuestions <= 0 {
		minQuestions = 3
	}
	minDaysActive := s.topicClusterMinDaysActive
	if minDaysActive <= 0 {
		minDaysActive = 7
	}

	var underfilled []wiki.TopicSignalUnderfilled
	for _, k := range order {
		a := groups[k]
		distinctQuestionCount := len(a.questions)
		daysActive := len(a.dates)
		if distinctQuestionCount < minQuestions || daysActive < minDaysActive {
			continue
		}

		fingerprint := topicFingerprint(k.subject, k.intent, k.audience, k.constraint)
		dup, err := s.store.HasNonRejectedTopicCandidate(fingerprint)
		if err != nil {
			slog.Error("study: check topic candidate dedup failed", "error", err)
			continue
		}
		if dup {
			continue
		}

		cand, uf, err := s.wikiSvc.DetectTopicCandidate(k.subject, k.intent, k.audience, k.constraint,
			distinctQuestionCount, daysActive, s.cfg.WikiKPMin)
		if err != nil {
			slog.Error("study: detect topic candidate failed", "error", err)
			continue
		}
		if uf != nil {
			underfilled = append(underfilled, *uf)
			continue
		}
		if cand == nil {
			// Cleared the stable-cluster gate but failed 步骤 7 二阶准入
			// (关联不够/整体可靠度不够) or had <2 published members -- Wiki
			// already logged the specific reason; nothing to persist here
			// (no page_id was minted to attach a pending_confirm result to).
			continue
		}

		lr := &activation.LearningResult{
			Action:     activation.ActionTopicPageCandidate,
			ObjectType: activation.ObjectTypeWikiPage,
			ObjectID:   cand.PageID,
			Reason:     fmt.Sprintf("[topic_fp:%s] %s", fingerprint, cand.Reason),
			EventIDs:   marshalIDs(cand.MemberPageIDs),
			Status:     activation.ResultPendingConfirm,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert topic_page_candidate learning result failed", "page_id", cand.PageID, "error", err)
			continue
		}
		actions.Actions = append(actions.Actions, LearningActionEntry{
			ResultID: lr.ResultID, Action: lr.Action, ObjectID: lr.ObjectID, Reason: lr.Reason, Status: lr.Status,
		})
	}
	return underfilled, nil
}

// topicFingerprint identifies a normalized quadruple for the
// topic_page_candidate dedup check (docs/impl/v1/wiki.md 步骤 8 "去重"): a
// short stable hash embedded in learning_results.reason (as "[topic_fp:...]"),
// since object_id for this action is the shell page_id -- naturally unique
// per candidate, not per quadruple -- and there's no dedicated quadruple
// table to key off of instead.
func topicFingerprint(subject, intent, audience, constraint string) string {
	h := sha1.Sum([]byte(subject + "\x1f" + intent + "\x1f" + audience + "\x1f" + constraint))
	return hex.EncodeToString(h[:])
}

// recomputePageRelations implements docs/impl/v1/wiki.md 步骤 7b: after new
// cross-Source KPN relations appear, recompute only the page pairs whose
// published concept pages cite one of the newly-related points — not a full
// pairwise rescan. "New" is tracked by an in-process watermark (the last
// time this cycle ran); a fresh process re-scans the whole history once on
// its first cycle, which is an acceptable one-time cost rather than a
// persisted cross-restart watermark.
func (s *Service) recomputePageRelations() error {
	if s.wikiSvc == nil {
		return nil
	}
	scanFrom := s.lastRelationScanAt
	now := time.Now().UTC()
	pointIDs, err := s.store.RecentCrossRelationPointIDs(scanFrom)
	if err != nil {
		return fmt.Errorf("study: recent cross relation point ids: %w", err)
	}
	s.lastRelationScanAt = now
	if len(pointIDs) == 0 {
		return nil
	}
	return s.wikiSvc.RecomputeRelationsForPoints(pointIDs)
}

// buildWikiDraftReflowSection implements docs/impl/v1/study.md 步骤 6's
// "wiki_draft_reflow" report item.
func (s *Service) buildWikiDraftReflowSection() ([]WikiDraftReflowEntry, error) {
	rows, err := s.store.WikiDraftReflowStats()
	if err != nil {
		return nil, err
	}
	out := make([]WikiDraftReflowEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, WikiDraftReflowEntry{
			SourceID: r.SourceID, OriginPageID: r.OriginPageID,
			ProducedKPCount: r.ProducedKPCount, SkippedAncestorEdges: r.SkippedAncestorEdges,
		})
	}
	return out, nil
}

// buildTopicDecomposeSection implements docs/impl/v1/study.md 步骤 6's
// "topic_decompose" report item: aggregate topic_decompose_signal events by
// the topic page that provided the skeleton.
func (s *Service) buildTopicDecomposeSection(windowDays int) ([]TopicDecomposeEntry, error) {
	rows, err := s.store.TopicDecomposeSignals(windowDays)
	if err != nil {
		return nil, err
	}
	type agg struct {
		count            int
		memberSum        int
		outsidePositives int
	}
	byPage := make(map[string]*agg)
	var order []string
	for _, r := range rows {
		a, ok := byPage[r.PageID]
		if !ok {
			a = &agg{}
			byPage[r.PageID] = a
			order = append(order, r.PageID)
		}
		a.count++
		a.memberSum += len(r.ResolvedMemberPageIDs)
		if r.ResolvedOutsideCount > 0 {
			a.outsidePositives++
		}
	}

	out := make([]TopicDecomposeEntry, 0, len(order))
	for _, pageID := range order {
		a := byPage[pageID]
		entry := TopicDecomposeEntry{PageID: pageID, SignalCount: a.count}
		if a.count > 0 {
			entry.AvgResolvedMemberCount = float64(a.memberSum) / float64(a.count)
			entry.OutsideRatioPositive = float64(a.outsidePositives) / float64(a.count)
		}
		out = append(out, entry)
	}
	return out, nil
}

// buildQuestionComplexitySection implements docs/impl/v1/study.md 步骤 7's
// "问题复杂度观测量": group traces by their four-tuple and compute per-group
// metrics. Report-only — never feeds any online routing decision.
//
// Grouping uses the raw (subject, intent, audience, constraint_text) tuple
// rather than re-running activation.Matcher's fuzzy/synonym condition-group
// matching: the doc calls for the same normalization the retrieval-side
// matcher uses so "same class of question" means one thing across both
// sides, but a full re-derivation of Matcher's grouping here would require
// loading the synonym resolver and re-running conditionGroupMatches per
// trace pair, which is a much larger change than this observation-only
// section's actual decision weight (it drives no behavior) justifies for
// V1. Revisit if the raw-tuple grouping turns out too fragmented to be
// useful once real data accumulates.
func (s *Service) buildQuestionComplexitySection(windowDays int) (QuestionComplexitySection, error) {
	rows, err := s.store.ComplexityTraces(windowDays)
	if err != nil {
		return QuestionComplexitySection{}, err
	}
	decomposeRows, err := s.store.TopicDecomposeSignals(windowDays)
	if err != nil {
		slog.Warn("study: fetch topic decompose signals for complexity failed", "error", err)
	}
	// topic_decompose_signal payloads don't carry trace_id in the decoded
	// struct (it's implicit via learning_events.trace_id, not part of the
	// payload JSON) — cross_member_ratio/outside_ratio are therefore computed
	// globally across the window rather than joined per group; still useful
	// as an overall signal, just not sliced by four-tuple in this cut.
	crossMemberTotal, outsideTotal := 0, 0
	for _, r := range decomposeRows {
		if len(r.ResolvedMemberPageIDs) >= 2 {
			crossMemberTotal++
		}
		if r.ResolvedOutsideCount > 0 {
			outsideTotal++
		}
	}
	var globalCrossMemberRatio, globalOutsideRatio float64
	if len(decomposeRows) > 0 {
		globalCrossMemberRatio = float64(crossMemberTotal) / float64(len(decomposeRows))
		globalOutsideRatio = float64(outsideTotal) / float64(len(decomposeRows))
	}

	type key struct{ subject, intent, audience, constraint string }
	type agg struct {
		count          int
		pathCounts     map[string]int
		directPointSum int
		wikiCount      int
		skeletonUsed   int
	}
	groups := make(map[key]*agg)
	var order []key
	for _, r := range rows {
		k := key{r.Subject, r.Intent, r.Audience, r.Constraint}
		a, ok := groups[k]
		if !ok {
			a = &agg{pathCounts: map[string]int{}}
			groups[k] = a
			order = append(order, k)
		}
		a.count++
		a.pathCounts[r.PathType]++
		a.directPointSum += r.DirectPointCount
		if r.PathType == "wiki" {
			a.wikiCount++
		}
		if r.SkeletonPageID != "" {
			a.skeletonUsed++
		}
	}

	minQuestions := s.cfg.ComplexityMinQuestions
	if minQuestions <= 0 {
		minQuestions = 3
	}

	var out []QuestionComplexityGroup
	for _, k := range order {
		a := groups[k]
		if a.count < minQuestions {
			continue
		}
		out = append(out, QuestionComplexityGroup{
			Subject: k.subject, Intent: k.intent, Audience: k.audience, Constraint: k.constraint,
			QuestionCount:       a.count,
			PathDistribution:    a.pathCounts,
			AvgDirectPointCount: float64(a.directPointSum) / float64(a.count),
			WikiSatisfiedRatio:  float64(a.wikiCount) / float64(a.count),
			SkeletonUsedCount:   a.skeletonUsed,
			CrossMemberRatio:    globalCrossMemberRatio,
			OutsideRatio:        globalOutsideRatio,
			ComplexityHint:      nil, // thresholds not calibrated yet (docs/impl/v1/study.md 步骤 7)
		})
	}
	return QuestionComplexitySection{Groups: out}, nil
}

// convergenceTrendMaxPoints caps how many prior reports' snapshots feed the
// "convergence" section's trend (docs/impl/v1/study.md 步骤 7,
// docs/design/activation-convergence.md 第 5 节) — a bounded, readable
// window rather than the full report history.
const convergenceTrendMaxPoints = 10

// buildConvergenceSection implements docs/impl/v1/study.md 步骤 7's
// "convergence" report item: this cycle's confidence width/tier snapshot,
// plus a trend against recent prior reports so a reader can see whether the
// distribution is narrowing and the exploration-budget share is shrinking
// over time, not just a single point-in-time number.
func (s *Service) buildConvergenceSection() (ConvergenceSection, error) {
	if s.activationSvc == nil {
		return ConvergenceSection{}, nil
	}
	current, err := s.store.ConfidenceConvergenceStats(s.activationSvc.ConfidenceConfig(), s.cfg.PruneWidthMax)
	if err != nil {
		return ConvergenceSection{}, err
	}
	trend, err := s.store.RecentConvergenceSnapshots(convergenceTrendMaxPoints)
	if err != nil {
		slog.Error("study: recent convergence snapshots failed", "error", err)
		trend = nil
	}
	return ConvergenceSection{Current: current, Trend: trend}, nil
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
		eventIDs, err := s.store.ActivationGapEventIDsForPoint(c.PointID)
		if err != nil {
			slog.Error("study: lookup activation_gap event ids failed",
				"point_id", c.PointID, "error", err)
			continue
		}
		created, err := s.tryCreateLink(c.QuestionTerms, c.PointID, eventIDs, reason)
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
// already exists for it — dedup is at point level. When the point already has
// a non-deprecated link, rebuild observed_conditions from confident traces
// (buildObservedConditions) instead of creating a second link.
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
		conds, err := s.buildObservedConditions(pointID)
		if err != nil || len(conds) == 0 {
			return false, err
		}
		if activation.ConditionsEqual(conds, existing.ObservedConditions) {
			return false, nil
		}
		return false, s.activationSvc.ReplaceObservedConditions(existing.LinkID, conds)
	}

	conds, err := s.buildObservedConditions(pointID)
	if err != nil || len(conds) == 0 {
		return false, err
	}

	link, err := s.activationSvc.CreateLink(questionTerms, activation.LinkCondition{ObservedConditions: conds}, pointID, createdFrom)
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

// buildObservedConditions turns every confident citing trace into an observed
// condition group (no keyword intersection / whitelist union).
func (s *Service) buildObservedConditions(pointID string) ([]activation.ObservedCondition, error) {
	quads, err := s.store.ConfidentTraceQuadruples(pointID)
	if err != nil {
		return nil, err
	}
	if len(quads) == 0 {
		return nil, nil
	}
	max := s.cfg.ObservedConditionsMax
	if max <= 0 {
		max = 50
	}
	var conds []activation.ObservedCondition
	for _, q := range quads {
		add := activation.NormalizeObservedCondition(q.Subject, q.Intent, q.Audience, q.Constraint, q.QuestionTerms, q.CreatedAt)
		conds = activation.MergeObservedConditions(conds, add, max)
	}
	return conds, nil
}

// evictIdle implements docs/impl/v1/study.md 步骤 4 (2026-08-13 起收窄:
// candidate/weakened 的离散 idle 淘汰随状态机一起移除 — 连续置信度下"该不该
// 服务"已经由 mean(cond) 持续表达，不需要另一条基于created_at/status_changed_at
// 的独立淘汰规则；question_tuple_norms 的 idle 清理与置信度机制无关，原样保留).
func (s *Service) evictIdle(actions *LearningActionsSummary) error {
	return s.evictIdleTupleNorms()
}

// pruneConditions implements docs/impl/v1/study.md 步骤 3「收敛剪枝」
// (docs/design/activation-convergence.md 第 11 节): scans every
// non-deprecated link's observed_conditions via store.PruneCandidateConditions,
// then for each flagged link removes exactly the identified conditions and
// persists through activation.Service.ReplaceObservedConditions — never via
// direct SQL here — so status re-derivation (deriveAndPersistStatus) and
// matcher cache invalidation stay on their one existing write path. Writes
// one learning_results(action=prune_condition) audit row per pruned link,
// not per condition — the reason string enumerates the classification
// breakdown.
func (s *Service) pruneConditions(actions *LearningActionsSummary) error {
	results, err := s.store.PruneCandidateConditions(
		s.cfg.PruneMeanMax, s.cfg.PruneWidthMax, s.cfg.PruneSampleMin, s.cfg.PruneIdleDays, s.cfg.PruneStaleDays)
	if err != nil {
		return fmt.Errorf("study: prune candidate conditions: %w", err)
	}

	for _, r := range results {
		link, err := s.activationSvc.Store().GetByID(r.LinkID)
		if err != nil {
			slog.Error("study: prune: load link failed", "link_id", r.LinkID, "error", err)
			continue
		}
		if link == nil {
			continue
		}

		toRemove := make(map[string]struct{}, len(r.Conditions))
		convergedLow, longIdle := 0, 0
		for _, c := range r.Conditions {
			toRemove[c.Subject+"\x1f"+c.Intent+"\x1f"+c.Audience+"\x1f"+c.Constraint] = struct{}{}
			switch c.Classification {
			case "converged_low":
				convergedLow++
			case "long_idle":
				longIdle++
			}
		}

		var trimmed []activation.ObservedCondition
		for _, c := range link.ObservedConditions {
			key := c.Subject + "\x1f" + c.Intent + "\x1f" + c.Audience + "\x1f" + c.Constraint
			if _, prune := toRemove[key]; prune {
				continue
			}
			trimmed = append(trimmed, c)
		}
		if trimmed == nil {
			trimmed = []activation.ObservedCondition{}
		}

		if err := s.activationSvc.ReplaceObservedConditions(r.LinkID, trimmed); err != nil {
			slog.Error("study: prune: replace observed conditions failed", "link_id", r.LinkID, "error", err)
			continue
		}

		reason := fmt.Sprintf("收敛剪枝：converged_low=%d, long_idle=%d，剩余观测条件 %d 条",
			convergedLow, longIdle, len(trimmed))
		lr := &activation.LearningResult{
			Action:     activation.ActionPruneCondition,
			ObjectType: activation.ObjectTypeActivationLink,
			ObjectID:   r.LinkID,
			Reason:     reason,
			Status:     activation.ResultApplied,
		}
		if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
			slog.Error("study: insert prune_condition learning result failed", "link_id", r.LinkID, "error", err)
			continue
		}

		actions.PrunedConditions += len(r.Conditions)
		actions.Actions = append(actions.Actions, LearningActionEntry{
			ResultID: lr.ResultID, Action: lr.Action, ObjectID: lr.ObjectID, Reason: lr.Reason, Status: lr.Status,
		})
	}
	return nil
}

// evictIdleTupleNorms cleans up question_tuple_norms rows whose last_hit_at
// is older than questionTupleNormIdleDays (docs/impl/v1/retrieval.md 步骤 2).
// <=0 (feature not configured / config-gated off) skips cleanup entirely —
// mirrors DeleteIdleOlderThan's own no-op guard, kept here too so the log
// line only fires when the feature is actually in use.
func (s *Service) evictIdleTupleNorms() error {
	if s.questionTupleNormIdleDays <= 0 || s.activationSvc == nil {
		return nil
	}
	n, err := s.activationSvc.CleanIdleTupleNorms(s.questionTupleNormIdleDays)
	if err != nil {
		slog.Error("study: clean idle question tuple norms failed", "error", err)
		return nil
	}
	if n > 0 {
		slog.Info("study: cleaned idle question tuple norms", "count", n)
	}
	return nil
}
