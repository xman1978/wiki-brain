package study

import (
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func testConfig() config.StudyConfig {
	return config.StudyConfig{
		ScheduleInterval:      "1h",
		CandidateConfidentMin: 5,
		CandidateRatioMin:     0.6,
		WikiKPMin:             4,
		WikiConfidentMin:      8,
		GapHitThreshold:       3,
		ScanBatchSize:         200,
		ReportPeriodDays:      30,
		ReportMaxKeep:         10,
	}
}

func TestService_Run_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig())

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
	svc := NewService(store, cfg)

	// Seed prerequisite data
	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "TestDomain")
	seedConcept(t, db, "con1", "dom1", "TestConcept")
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

func TestService_GapThresholdWarning(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := testConfig()
	cfg.GapHitThreshold = 2
	svc := NewService(store, cfg)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
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
	svc := NewService(store, cfg)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")

	// High purity + needs breadth ≥ 3
	seedCooccurrence(t, db, "t1", "kp1", 10, 8)  // purity 0.8
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
	cfg.WikiConfidentMin = 5
	svc := NewService(store, cfg)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "TestConcept")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKPRelation(t, db, "kp1", "kp2")

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
	if w.ConceptID != "con1" {
		t.Errorf("expected concept_id=con1, got %s", w.ConceptID)
	}
	if w.Stats.QualifyingKPCount != 2 {
		t.Errorf("expected 2 qualifying KPs, got %d", w.Stats.QualifyingKPCount)
	}
	if w.Stats.KPNConnectionCount != 1 {
		t.Errorf("expected 1 KPN connection, got %d", w.Stats.KPNConnectionCount)
	}
	if w.Recommendation != "ready" {
		t.Errorf("expected ready, got %s", w.Recommendation)
	}
}
