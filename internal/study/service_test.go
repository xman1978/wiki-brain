package study

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

// newTestActivationSvc wires an activation.Service against the same *sql.DB
// study is using, so both packages see the same activation_links /
// learning_results rows.
func newTestActivationSvc(db *sql.DB) *activation.Service {
	store := activation.NewStore(db)
	svc := activation.NewService(store, activation.NewMatcher(store))
	svc.SetConfidenceConfig(testConfidenceConfig())
	return svc
}

func testConfig() config.StudyConfig {
	return config.StudyConfig{
		ScheduleInterval:    "1h",
		CreateConfidenceMin: 0.55,
		CreateWidthMax:      0.03,
		WikiKPMin:           4,
		GapHitThreshold:     3,
		ScanBatchSize:       200,
		ReportPeriodDays:    30,
		ReportMaxKeep:       10,
		EventWindowDays:     30,
		CorrectionWeight:    2,
		PruneMeanMax:        0.3,
		PruneWidthMax:       0.02,
		PruneSampleMin:      8,
		PruneIdleDays:       30,
		PruneStaleDays:      90,
	}
}

// testConfidenceConfig is a permissive ConfidenceConfig for tests that need
// a link to land on verified deterministically: any success_count >= 1
// clears mean 2/3 >= 0.5, and 0 audit_sample_min means self_graded is
// reachable immediately (never trusted without independent verification,
// but self_graded already counts as "verified" per deriveStatus).
func testConfidenceConfig() activation.ConfidenceConfig {
	return activation.ConfidenceConfig{
		ServingConfidenceMin:  0.5,
		AuditSampleMin:        5,
		ExploreRateLow:        1.0,
		ExploreRateSelfGraded: 0,
		ExploreRateTrusted:    0,
	}
}

func TestService_Run_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, testConfig(), newTestActivationSvc(db), nil, 0, 0, 0)

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
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, 0)

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
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, 0)

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
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, 0)

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
	store.ScanCandidates(cfg.CreateConfidenceMin, cfg.CreateWidthMax, cfg.ScanBatchSize)

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
