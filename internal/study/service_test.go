package study

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

// newTestActivationSvc wires an activation.Service against the same *sql.DB
// study is using, so both packages see the same activation_links /
// learning_results rows.
func newTestActivationSvc(db *sql.DB) *activation.Service {
	store := activation.NewStore(db)
	return activation.NewService(store, activation.NewMatcher(store))
}

func testConfig() config.StudyConfig {
	return config.StudyConfig{
		ScheduleInterval:      "1h",
		CandidateConfidentMin: 5,
		CandidateRatioMin:     0.6,
		WikiKPMin:             4,
		GapHitThreshold:       3,
		ScanBatchSize:         200,
		ReportPeriodDays:      30,
		ReportMaxKeep:         10,
		AutoPromote:           false,
		PromoteSuccessMin:     3,
		PromoteDistinctMin:    2,
		WeakenFailureMin:      3,
		WeakenRatioMin:        0.5,
		ReverifySuccessMin:    2,
		EventWindowDays:       30,
		CandidateIdleDays:     30,
		DeprecateIdleDays:     60,
		CorrectionWeight:      2,
	}
}

func TestService_Run_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig(), newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CandidatesFlagged != 0 {
		t.Errorf("expected 0 candidates, got %d", result.CandidatesFlagged)
	}
	if result.GapEventsProcessed != 0 {
		t.Errorf("expected 0 gap events, got %d", result.GapEventsProcessed)
	}
	if result.ReportID == "" {
		t.Error("expected non-empty report_id")
	}

	// Verify report saved
	content, err := store.GetReport(result.ReportID)
	if err != nil || content == nil {
		t.Fatal("report not saved")
	}

	var report Report
	if err := json.Unmarshal([]byte(*content), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.Summary.TotalTraces != 0 {
		t.Errorf("expected 0 total_traces, got %d", report.Summary.TotalTraces)
	}
}

func TestService_Run_WithData(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	// Seed prerequisite data
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "TestDomain")
	seedEntry(t, db, "con1", "dom1", "TestEntry")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "知识点1")
	seedKP(t, db, "kp2", "ku1", "src1", "知识点2")
	seedKPRelation(t, db, "kp1", "kp2")

	// Cooccurrence that meets thresholds
	seedCooccurrence(t, db, "golang 并发", "kp1", 10, 8)

	// Traces
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "什么是并发", "golang 并发", "confident", "short", []string{"kp1"})

	// Gap events
	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr2", "ans2", "什么是分布式", "分布式", "gap", "short", []string{})
	payload, _ := json.Marshal(map[string]string{"question": "什么是分布式"})
	seedLearningEvent(t, db, "evt1", "tr2", "knowledge_gap", string(payload))

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CandidatesFlagged != 1 {
		t.Errorf("expected 1 candidate, got %d", result.CandidatesFlagged)
	}
	if result.GapEventsProcessed != 1 {
		t.Errorf("expected 1 gap event, got %d", result.GapEventsProcessed)
	}

	// Verify report content
	content, _ := store.GetReport(result.ReportID)
	var report Report
	json.Unmarshal([]byte(*content), &report)

	if len(report.ActivationLinkCandidates) != 1 {
		t.Errorf("expected 1 activation candidate, got %d", len(report.ActivationLinkCandidates))
	}
	if len(report.ActivationLinkCandidates) > 0 {
		c := report.ActivationLinkCandidates[0]
		if c.Stats.SignalPurity != 0.8 {
			t.Errorf("expected signal_purity=0.8, got %f", c.Stats.SignalPurity)
		}
		if !c.Stats.HasKPNNeighbors {
			t.Error("expected has_kpn_neighbors=true")
		}
	}

	if len(report.KnowledgeGaps) != 1 {
		t.Errorf("expected 1 gap entry, got %d", len(report.KnowledgeGaps))
	}
}

func TestGapEntryFromRow_RecommendationByLastReason(t *testing.T) {
	cases := []struct {
		lastReason string
		want       string
	}{
		{"no_candidates", "补充材料"},
		{"judge_filtered", "语义提取待核对"},
		{"answer_error", "生成异常，需查日志"},
		{"unspecified", "补充材料"},
		{"", "补充材料"}, // pre-migration rows
	}
	for _, c := range cases {
		row := KnowledgeGapRow{
			QuestionTerms:    "q",
			Question:         "问题",
			HitCount:         3,
			ReasonCountsJSON: `{"` + c.lastReason + `":3}`,
			LastReason:       c.lastReason,
			LastTraceID:      "tr1",
		}
		entry := gapEntryFromRow(row)
		if entry.Recommendation != c.want {
			t.Errorf("last_reason=%q: expected recommendation=%q, got %q", c.lastReason, c.want, entry.Recommendation)
		}
		if entry.LastTraceID != "tr1" {
			t.Errorf("expected last_trace_id passed through, got %q", entry.LastTraceID)
		}
	}
}

func TestService_GapThresholdWarning(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.GapHitThreshold = 2
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c")

	seedAnswer(t, db, "ans1")
	seedAnswer(t, db, "ans2")
	seedTrace(t, db, "tr1", "ans1", "q1", "terms1", "gap", "short", []string{})
	seedTrace(t, db, "tr2", "ans2", "q2", "terms1", "gap", "short", []string{})

	payload, _ := json.Marshal(map[string]string{"question": "q1"})
	seedLearningEvent(t, db, "evt1", "tr1", "knowledge_gap", string(payload))
	payload2, _ := json.Marshal(map[string]string{"question": "q2"})
	seedLearningEvent(t, db, "evt2", "tr2", "knowledge_gap", string(payload2))

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.GapEventsProcessed != 2 {
		t.Errorf("expected 2 gap events, got %d", result.GapEventsProcessed)
	}
}

func TestService_RecommendationLogic(t *testing.T) {
	// Test strong vs candidate recommendation
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")

	// High purity + needs breadth ≥ 3
	seedCooccurrence(t, db, "t1", "kp1", 10, 8) // purity 0.8
	seedCooccurrence(t, db, "t2", "kp1", 5, 4)
	seedCooccurrence(t, db, "t3", "kp1", 3, 2)

	// Traces for short_path_rate
	seedAnswer(t, db, "a1")
	seedAnswer(t, db, "a2")
	seedAnswer(t, db, "a3")
	seedTrace(t, db, "tr1", "a1", "q1", "t1", "confident", "short", []string{"kp1"})
	seedTrace(t, db, "tr2", "a2", "q2", "t2", "confident", "short", []string{"kp1"})
	seedTrace(t, db, "tr3", "a3", "q3", "t3", "confident", "long", []string{"kp1"})

	// Scan candidates first
	store.ScanCandidates(cfg.CandidateConfidentMin, cfg.CandidateRatioMin, cfg.ScanBatchSize)

	candidates, err := svc.buildActivationCandidates(cfg.ReportPeriodDays)
	if err != nil {
		t.Fatalf("buildActivationCandidates: %v", err)
	}

	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}

	c := candidates[0]
	// breadth = 3 (t1,t2,t3 all have confident_count > 0)
	// short_path_rate = 2/3 ≈ 0.667
	// signal_purity = 8/10 = 0.8
	// All ≥ thresholds → strong
	if c.Recommendation != "strong" {
		t.Errorf("expected strong, got %s (purity=%.2f breadth=%d spr=%.2f)",
			c.Recommendation, c.Stats.SignalPurity, c.Stats.ActivationBreadth, c.Stats.ShortPathRate)
	}
}

func TestService_WikiCandidates(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.WikiKPMin = 2
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "TestEntry")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKPRelation(t, db, "kp1", "kp2")
	seedVerifiedActivationLink(t, db, "link1", "kp1")
	seedVerifiedActivationLink(t, db, "link2", "kp2")

	// Insert candidates with high confident_count
	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc1', 't1', 'kp1', 10, 12)`)
	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc2', 't2', 'kp2', 8, 10)`)

	wikis, err := svc.buildWikiCandidates()
	if err != nil {
		t.Fatalf("buildWikiCandidates: %v", err)
	}

	if len(wikis) != 1 {
		t.Fatalf("expected 1 wiki candidate, got %d", len(wikis))
	}

	w := wikis[0]
	if w.EntryID != "con1" {
		t.Errorf("expected entry_id=con1, got %s", w.EntryID)
	}
	if w.Stats.QualifyingKPCount != 2 {
		t.Errorf("expected 2 qualifying KPs, got %d", w.Stats.QualifyingKPCount)
	}
	if w.Stats.KPNConnectionCount != 1 {
		t.Errorf("expected 1 KPN connection, got %d", w.Stats.KPNConnectionCount)
	}
	if w.Stats.RelatedConnectionCount != 1 || w.Stats.ContradictsConnectionCount != 0 {
		t.Errorf("expected related=1/contradicts=0, got related=%d/contradicts=%d",
			w.Stats.RelatedConnectionCount, w.Stats.ContradictsConnectionCount)
	}
	if w.Recommendation != "ready" {
		t.Errorf("expected ready, got %s", w.Recommendation)
	}
}

// TestService_WikiCandidates_ContradictsDominantNotReady covers
// docs/design/wiki-compilation.md "ActivationLink 回答'这条管不管用'，Wiki
// 编译回答'这个主题够不够格立传'": a topic whose KPN connections are mostly
// contradicts (not settled enough to consolidate) must not be recommended
// ready, even though qualifying_kp_count and days_active would otherwise pass.
func TestService_WikiCandidates_ContradictsDominantNotReady(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.WikiKPMin = 2
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "TestEntry")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKP(t, db, "kp3", "ku1", "src1", "c3")
	// One related connection, two contradicts — contradicts dominates.
	seedKPRelation(t, db, "kp1", "kp2")
	if _, err := db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, prompt_version)
		VALUES ('rel-c1', 'kp1', 'kp3', 'contradicts', 'v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, prompt_version)
		VALUES ('rel-c2', 'kp2', 'kp3', 'contradicts', 'v1')`); err != nil {
		t.Fatal(err)
	}
	seedVerifiedActivationLink(t, db, "link1", "kp1")
	seedVerifiedActivationLink(t, db, "link2", "kp2")
	seedVerifiedActivationLink(t, db, "link3", "kp3")

	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc1', 't1', 'kp1', 10, 12)`)
	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc2', 't2', 'kp2', 8, 10)`)
	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc3', 't3', 'kp3', 8, 10)`)

	wikis, err := svc.buildWikiCandidates()
	if err != nil {
		t.Fatalf("buildWikiCandidates: %v", err)
	}
	if len(wikis) != 1 {
		t.Fatalf("expected 1 wiki candidate, got %d", len(wikis))
	}
	w := wikis[0]
	if w.Stats.RelatedConnectionCount != 1 || w.Stats.ContradictsConnectionCount != 2 {
		t.Fatalf("expected related=1/contradicts=2, got related=%d/contradicts=%d",
			w.Stats.RelatedConnectionCount, w.Stats.ContradictsConnectionCount)
	}
	if w.Recommendation != "needs_more_data" {
		t.Errorf("expected needs_more_data when contradicts dominates related, got %s", w.Recommendation)
	}
}

// TestService_WikiCandidates_LowCohesionSplitSignal covers docs/design/
// wiki-compilation.md "连贯性判断还需要第三层" and docs/impl/v1/
// wiki-generation.md 2.4: a concept whose qualifying KPs form two internally
// related but mutually disconnected pairs clears breadth/related/stable yet
// isn't one coherent topic. With the cohesion gate off (CohesionConfig{},
// what every other test in this file uses) it must still be recommended
// ready — this is what keeps the gate's addition from silently changing
// every pre-existing "ready" expectation. With the gate configured on, the
// same data must flip to needs_more_data and produce a EntrySplitSignalEntry
// naming both clusters.
func TestService_WikiCandidates_LowCohesionSplitSignal(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.WikiKPMin = 2

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "TestEntry")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKP(t, db, "kp3", "ku1", "src1", "c3")
	seedKP(t, db, "kp4", "ku1", "src1", "c4")
	// Two internally related pairs, no edge between them at all.
	seedKPRelation(t, db, "kp1", "kp2")
	seedKPRelation(t, db, "kp3", "kp4")
	seedVerifiedActivationLink(t, db, "link1", "kp1")
	seedVerifiedActivationLink(t, db, "link2", "kp2")
	seedVerifiedActivationLink(t, db, "link3", "kp3")
	seedVerifiedActivationLink(t, db, "link4", "kp4")
	for i, pid := range []string{"kp1", "kp2", "kp3", "kp4"} {
		if _, err := db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES (?, ?, ?, ?, ?)`,
			fmt.Sprintf("lc%d", i), fmt.Sprintf("t%d", i), pid, 10, 12); err != nil {
			t.Fatal(err)
		}
	}

	svcGateOff := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{}, 0, 0)
	wikisOff, _, err := svcGateOff.buildWikiCandidatesWithSplitSignals()
	if err != nil {
		t.Fatalf("buildWikiCandidatesWithSplitSignals (gate off): %v", err)
	}
	if len(wikisOff) != 1 || wikisOff[0].Recommendation != "ready" {
		t.Fatalf("gate off: expected 1 ready candidate despite low cohesion, got %+v", wikisOff)
	}
	if wikisOff[0].Stats.Cohesion >= 0.99 {
		t.Errorf("gate off: expected Stats.Cohesion to still report the low value (~0.5), got %.2f", wikisOff[0].Stats.Cohesion)
	}

	svcGateOn := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, CohesionConfig{Min: 0.6, WRel: 1.0, Gamma: 1.0}, 0, 0)
	wikisOn, splitSignals, err := svcGateOn.buildWikiCandidatesWithSplitSignals()
	if err != nil {
		t.Fatalf("buildWikiCandidatesWithSplitSignals (gate on): %v", err)
	}
	if len(wikisOn) != 1 || wikisOn[0].Recommendation != "needs_more_data" {
		t.Fatalf("gate on: expected 1 needs_more_data candidate, got %+v", wikisOn)
	}
	if len(splitSignals) != 1 {
		t.Fatalf("expected 1 concept split signal, got %d", len(splitSignals))
	}
	sig := splitSignals[0]
	if sig.EntryID != "con1" {
		t.Errorf("split signal entry_id = %q, want con1", sig.EntryID)
	}
	if sig.AspectCount != 2 || len(sig.Communities) != 2 {
		t.Fatalf("expected 2 communities, got %+v", sig.Communities)
	}
	for _, c := range sig.Communities {
		if len(c.PointIDs) != 2 {
			t.Errorf("community %+v: expected 2 members, got %d", c, len(c.PointIDs))
		}
	}
}

// TestService_FlagWikiCandidates_RecordsEventIDs covers a P10 audit gap
// found in V1 testing (见 memory v1-p4-p10-test-findings): every Wiki action's
// learning_result must carry object_id/reason/event_ids, but flagWikiCandidates
// left event_ids empty.
func TestService_FlagWikiCandidates_RecordsEventIDs(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.WikiKPMin = 2
	activationSvc := newTestActivationSvc(db)
	svc := NewService(store, cfg, activationSvc, nil, 0, 0, CohesionConfig{}, 0, 0)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "TestEntry")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKPRelation(t, db, "kp1", "kp2")
	seedVerifiedActivationLink(t, db, "link1", "kp1")
	seedVerifiedActivationLink(t, db, "link2", "kp2")

	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc1', 't1', 'kp1', 10, 12)`)
	db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count) VALUES ('lc2', 't2', 'kp2', 8, 10)`)

	if err := svc.flagWikiCandidates(); err != nil {
		t.Fatalf("flagWikiCandidates: %v", err)
	}

	results, err := activationSvc.Store().ListLearningResultsByObject("wiki_page", "con1")
	if err != nil {
		t.Fatalf("ListLearningResultsByObject: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 learning result, got %d", len(results))
	}
	var eventIDs []string
	if err := json.Unmarshal([]byte(results[0].EventIDs), &eventIDs); err != nil {
		t.Fatalf("unmarshal event_ids %q: %v", results[0].EventIDs, err)
	}
	if len(eventIDs) != 2 {
		t.Errorf("expected 2 event_ids (kp1, kp2), got %v", eventIDs)
	}
}
