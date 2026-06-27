package study

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := foundation.NewTestDB(t)
	// Insert prerequisite data for foreign key constraints
	return db
}

func seedDomain(t *testing.T, db *sql.DB, domainID, name string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO domains (domain_id, name) VALUES (?, ?)`, domainID, name)
	if err != nil {
		t.Fatalf("seed domain: %v", err)
	}
}

func seedConcept(t *testing.T, db *sql.DB, conceptID, domainID, name string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO concepts (concept_id, domain_id, name) VALUES (?, ?, ?)`,
		conceptID, domainID, name)
	if err != nil {
		t.Fatalf("seed concept: %v", err)
	}
}

func seedSource(t *testing.T, db *sql.DB, sourceID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status) VALUES (?, 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'done')`,
		sourceID)
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func seedKU(t *testing.T, db *sql.DB, unitID, sourceID, conceptID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, concept_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, ?, 'test topic', 1, 10, 'done', 'v1')`,
		unitID, sourceID, conceptID)
	if err != nil {
		t.Fatalf("seed KU: %v", err)
	}
}

func seedKP(t *testing.T, db *sql.DB, pointID, unitID, sourceID, content string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES (?, ?, ?, ?, 'fact')`, pointID, unitID, sourceID, content)
	if err != nil {
		t.Fatalf("seed KP: %v", err)
	}
}

func seedCooccurrence(t *testing.T, db *sql.DB, questionTerms, pointID string, hitCount, confidentCount int) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO question_kp_cooccurrence (cooc_id, question_terms, point_id, hit_count, confident_count, last_seen_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		uuid.New().String(), questionTerms, pointID, hitCount, confidentCount)
	if err != nil {
		t.Fatalf("seed cooccurrence: %v", err)
	}
}

func seedAnswer(t *testing.T, db *sql.DB, answerID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name) VALUES (?, 'q', 'a', 'short', 'v1', 'test')`, answerID)
	if err != nil {
		t.Fatalf("seed answer: %v", err)
	}
}

func seedTrace(t *testing.T, db *sql.DB, traceID, answerID, question, questionTerms, quality, path string, pointIDs []string) {
	t.Helper()
	pidsJSON, _ := json.Marshal(pointIDs)
	_, err := db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		traceID, answerID, question, "hash_"+traceID, questionTerms, quality, path, string(pidsJSON))
	if err != nil {
		t.Fatalf("seed trace: %v", err)
	}
}

func seedLearningEvent(t *testing.T, db *sql.DB, eventID, traceID, eventType, payload string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO learning_events (event_id, trace_id, event_type, payload) VALUES (?, ?, ?, ?)`,
		eventID, traceID, eventType, payload)
	if err != nil {
		t.Fatalf("seed learning event: %v", err)
	}
}

func seedKPRelation(t *testing.T, db *sql.DB, sourcePointID, targetPointID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, prompt_version)
		VALUES (?, ?, ?, 'related', 'v1')`, uuid.New().String(), sourcePointID, targetPointID)
	if err != nil {
		t.Fatalf("seed KP relation: %v", err)
	}
}

// ========== Step 1: 共现扫描器 ==========

func TestScanCandidates_Basic(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "TestDomain")
	seedConcept(t, db, "con1", "dom1", "TestConcept")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "point content 1")
	seedKP(t, db, "kp2", "ku1", "src1", "point content 2")

	// kp1: confident_count=6, hit_count=8 → ratio 0.75 ≥ 0.6 ✓, confident ≥ 5 ✓
	seedCooccurrence(t, db, "golang 并发", "kp1", 8, 6)
	// kp2: confident_count=3, hit_count=5 → ratio 0.6 ✓, but confident < 5 ✗
	seedCooccurrence(t, db, "python 异步", "kp2", 5, 3)

	count, err := store.ScanCandidates(5, 0.6, 200)
	if err != nil {
		t.Fatalf("ScanCandidates: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 candidate, got %d", count)
	}

	// Verify link_candidates row
	var qterms, pid string
	var cc, hc int
	err = db.QueryRow(`SELECT question_terms, point_id, confident_count, hit_count FROM link_candidates`).
		Scan(&qterms, &pid, &cc, &hc)
	if err != nil {
		t.Fatalf("query link_candidates: %v", err)
	}
	if qterms != "golang 并发" || pid != "kp1" || cc != 6 || hc != 8 {
		t.Errorf("unexpected candidate: terms=%s pid=%s cc=%d hc=%d", qterms, pid, cc, hc)
	}
}

func TestScanCandidates_Upsert(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "TestDomain")
	seedConcept(t, db, "con1", "dom1", "TestConcept")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	seedCooccurrence(t, db, "terms1", "kp1", 10, 7)

	// First scan
	store.ScanCandidates(5, 0.6, 200)

	// Update cooccurrence
	db.Exec(`UPDATE question_kp_cooccurrence SET confident_count = 9, hit_count = 12 WHERE point_id = 'kp1'`)

	// Second scan should update
	count, err := store.ScanCandidates(5, 0.6, 200)
	if err != nil {
		t.Fatalf("ScanCandidates upsert: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	var cc int
	db.QueryRow(`SELECT confident_count FROM link_candidates WHERE point_id = 'kp1'`).Scan(&cc)
	if cc != 9 {
		t.Errorf("expected confident_count=9 after upsert, got %d", cc)
	}
}

func TestScanCandidates_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	count, err := store.ScanCandidates(5, 0.6, 200)
	if err != nil {
		t.Fatalf("ScanCandidates empty: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// ========== Step 2: 知识盲区聚合器 ==========

func TestGapAggregation(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedAnswer(t, db, "ans1")
	seedTrace(t, db, "tr1", "ans1", "什么是并发", "并发", "gap", "short", []string{})

	payload, _ := json.Marshal(map[string]string{"question": "什么是并发"})
	seedLearningEvent(t, db, "evt1", "tr1", "knowledge_gap", string(payload))

	events, err := store.FetchUnprocessedGapEvents()
	if err != nil {
		t.Fatalf("FetchUnprocessedGapEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].QuestionTerms != "并发" {
		t.Errorf("expected question_terms=并发, got %s", events[0].QuestionTerms)
	}

	hitCount, err := store.UpsertKnowledgeGap("并发", "什么是并发")
	if err != nil {
		t.Fatalf("UpsertKnowledgeGap: %v", err)
	}
	if hitCount != 1 {
		t.Errorf("expected hit_count=1, got %d", hitCount)
	}

	// Second upsert increments
	hitCount, err = store.UpsertKnowledgeGap("并发", "什么是并发模型")
	if err != nil {
		t.Fatalf("UpsertKnowledgeGap 2nd: %v", err)
	}
	if hitCount != 2 {
		t.Errorf("expected hit_count=2, got %d", hitCount)
	}

	// Verify question updated
	var q string
	db.QueryRow(`SELECT question FROM knowledge_gaps WHERE question_terms = '并发'`).Scan(&q)
	if q != "什么是并发模型" {
		t.Errorf("expected updated question, got %s", q)
	}

	if err := store.MarkEventProcessed("evt1"); err != nil {
		t.Fatalf("MarkEventProcessed: %v", err)
	}

	// Should no longer appear
	events, _ = store.FetchUnprocessedGapEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 unprocessed events, got %d", len(events))
	}
}

// ========== Step 3: 报告生成相关查询 ==========

func TestQueryTraceSummary(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")
	seedAnswer(t, db, "ans1")
	seedAnswer(t, db, "ans2")
	seedAnswer(t, db, "ans3")

	seedTrace(t, db, "t1", "ans1", "q1", "terms1", "confident", "short", []string{"kp1"})
	seedTrace(t, db, "t2", "ans2", "q2", "terms2", "partial", "short", []string{})
	seedTrace(t, db, "t3", "ans3", "q3", "terms3", "gap", "short", []string{})

	summary, err := store.QueryTraceSummary(30)
	if err != nil {
		t.Fatalf("QueryTraceSummary: %v", err)
	}
	if summary.TotalTraces != 3 {
		t.Errorf("expected 3 total traces, got %d", summary.TotalTraces)
	}
	if summary.ConfidentCount != 1 {
		t.Errorf("expected 1 confident, got %d", summary.ConfidentCount)
	}
	if summary.PartialCount != 1 {
		t.Errorf("expected 1 partial, got %d", summary.PartialCount)
	}
	if summary.GapCount != 1 {
		t.Errorf("expected 1 gap, got %d", summary.GapCount)
	}
}

func TestActivationBreadth(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	seedCooccurrence(t, db, "terms1", "kp1", 5, 3)
	seedCooccurrence(t, db, "terms2", "kp1", 4, 2)
	seedCooccurrence(t, db, "terms3", "kp1", 3, 0) // confident_count=0, not counted

	breadth, err := store.ActivationBreadth("kp1")
	if err != nil {
		t.Fatalf("ActivationBreadth: %v", err)
	}
	if breadth != 2 {
		t.Errorf("expected breadth=2, got %d", breadth)
	}
}

func TestShortPathRate(t *testing.T) {
	traces := []TracePathRow{
		{Path: "short", DirectPointIDsJSON: `["kp1","kp2"]`},
		{Path: "long", DirectPointIDsJSON: `["kp1"]`},
		{Path: "short", DirectPointIDsJSON: `["kp1"]`},
		{Path: "short", DirectPointIDsJSON: `["kp3"]`}, // doesn't contain kp1
	}

	rate := calcShortPathRate("kp1", traces)
	// 3 traces contain kp1, 2 are short → 2/3 ≈ 0.667
	expected := 2.0 / 3.0
	if rate < expected-0.01 || rate > expected+0.01 {
		t.Errorf("expected short_path_rate ≈ %.3f, got %.3f", expected, rate)
	}
}

func TestShortPathRate_Empty(t *testing.T) {
	rate := calcShortPathRate("kp1", nil)
	if rate != 0 {
		t.Errorf("expected 0, got %f", rate)
	}
}

func TestHasKPNNeighbors(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKP(t, db, "kp3", "ku1", "src1", "c3")

	// kp1 has a neighbor, kp3 doesn't
	seedKPRelation(t, db, "kp1", "kp2")

	has, err := store.HasKPNNeighbors("kp1")
	if err != nil {
		t.Fatalf("HasKPNNeighbors: %v", err)
	}
	if !has {
		t.Error("expected kp1 to have neighbors")
	}

	has, _ = store.HasKPNNeighbors("kp3")
	if has {
		t.Error("expected kp3 to have no neighbors")
	}
}

func TestKPNConnectionCount(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedConcept(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "c1")
	seedKP(t, db, "kp2", "ku1", "src1", "c2")
	seedKP(t, db, "kp3", "ku1", "src1", "c3")

	seedKPRelation(t, db, "kp1", "kp2")
	seedKPRelation(t, db, "kp2", "kp3")

	count, err := store.KPNConnectionCount([]string{"kp1", "kp2", "kp3"})
	if err != nil {
		t.Fatalf("KPNConnectionCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 connections, got %d", count)
	}

	count, _ = store.KPNConnectionCount([]string{"kp1"})
	if count != 0 {
		t.Errorf("expected 0 for single point, got %d", count)
	}
}

// ========== Report persistence ==========

func TestReportPersistence(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	report := Report{
		ReportID:    "rpt1",
		GeneratedAt: time.Now().UTC(),
		PeriodDays:  30,
		Summary:     TraceSummary{TotalTraces: 10},
	}
	content, _ := json.Marshal(report)

	if err := store.SaveReport("rpt1", 30, string(content)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := store.GetReport("rpt1")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got == nil {
		t.Fatal("expected report, got nil")
	}

	latest, err := store.GetLatestReport()
	if err != nil {
		t.Fatalf("GetLatestReport: %v", err)
	}
	if latest == nil {
		t.Fatal("expected latest report, got nil")
	}

	reports, err := store.ListReports()
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("expected 1 report, got %d", len(reports))
	}
}

func TestCleanOldReports(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	for i := 0; i < 5; i++ {
		id := uuid.New().String()
		content, _ := json.Marshal(Report{ReportID: id, PeriodDays: 30})
		store.SaveReport(id, 30, string(content))
	}

	if err := store.CleanOldReports(3); err != nil {
		t.Fatalf("CleanOldReports: %v", err)
	}

	reports, _ := store.ListReports()
	if len(reports) != 3 {
		t.Errorf("expected 3 reports after cleanup, got %d", len(reports))
	}
}

func TestGetReport_NotFound(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	got, err := store.GetReport("nonexistent")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent report")
	}
}

func TestGetLatestReport_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	got, err := store.GetLatestReport()
	if err != nil {
		t.Fatalf("GetLatestReport: %v", err)
	}
	if got != nil {
		t.Error("expected nil when no reports exist")
	}
}
