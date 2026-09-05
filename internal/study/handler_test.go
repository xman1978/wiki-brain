package study

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func setupHandler(t *testing.T) (*Handler, *Service) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)
	cfg := config.StudyConfig{
		CreateConfidenceMin: 0.55,
		CreateWidthMax:      0.03,
		WikiKPMin:           4,
		GapHitThreshold:     3,
		ScanBatchSize:       200,
		ReportPeriodDays:    30,
		ReportMaxKeep:       10,
		PruneMeanMax:        0.3,
		PruneWidthMax:       0.02,
		PruneSampleMin:      8,
		PruneIdleDays:       30,
		PruneStaleDays:      90,
	}
	svc := NewService(store, cfg, newTestActivationSvc(db), nil, 0, 0, 0, false, 0, 0)
	handler := NewHandler(svc)
	return handler, svc
}

func TestHandler_RunStudy(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/study/run", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result RunResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.ReportID == "" {
		t.Error("expected non-empty report_id")
	}
}

func TestHandler_ListReports_Empty(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/reports", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var reports []ReportMeta
	json.NewDecoder(w.Body).Decode(&reports)
	if len(reports) != 0 {
		t.Errorf("expected 0 reports, got %d", len(reports))
	}
}

func TestHandler_GetLatestReport_Empty(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/reports/latest", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandler_GetReport_NotFound(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/reports/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandler_RunThenGetReport(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Run study
	req := httptest.NewRequest("POST", "/study/run", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var result RunResult
	json.NewDecoder(w.Body).Decode(&result)

	// Get latest
	req = httptest.NewRequest("GET", "/study/reports/latest", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var report Report
	json.NewDecoder(w.Body).Decode(&report)
	if report.ReportID != result.ReportID {
		t.Errorf("expected report_id=%s, got %s", result.ReportID, report.ReportID)
	}

	// Get by ID
	req = httptest.NewRequest("GET", "/study/reports/"+result.ReportID, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// List reports
	req = httptest.NewRequest("GET", "/study/reports", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var reports []ReportMeta
	json.NewDecoder(w.Body).Decode(&reports)
	if len(reports) != 1 {
		t.Errorf("expected 1 report, got %d", len(reports))
	}
}

func TestHandler_Candidates_Empty(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/candidates", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandler_Gaps_Empty(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/gaps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandler_Gaps_ReasonFilterAndFields(t *testing.T) {
	handler, svc := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	if _, _, err := svc.store.UpsertKnowledgeGap("terms-a", "问题A", "no_candidates", "tr-a"); err != nil {
		t.Fatalf("seed gap a: %v", err)
	}
	if _, _, err := svc.store.UpsertKnowledgeGap("terms-b", "问题B", "judge_filtered", "tr-b"); err != nil {
		t.Fatalf("seed gap b: %v", err)
	}

	req := httptest.NewRequest("GET", "/study/gaps?reason=judge_filtered", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var gaps []KnowledgeGapEntry
	if err := json.Unmarshal(w.Body.Bytes(), &gaps); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap filtered by reason=judge_filtered, got %d", len(gaps))
	}
	g := gaps[0]
	if g.QuestionTerms != "terms-b" {
		t.Errorf("expected terms-b, got %s", g.QuestionTerms)
	}
	if g.LastReason != "judge_filtered" {
		t.Errorf("expected last_reason=judge_filtered, got %s", g.LastReason)
	}
	if g.LastTraceID != "tr-b" {
		t.Errorf("expected last_trace_id=tr-b, got %s", g.LastTraceID)
	}
	if g.Recommendation != "语义提取待核对" {
		t.Errorf("expected recommendation=语义提取待核对, got %s", g.Recommendation)
	}
	if g.ReasonCounts["judge_filtered"] != 1 {
		t.Errorf("expected reason_counts[judge_filtered]=1, got %v", g.ReasonCounts)
	}
}

func TestHandler_DomainCorrections_Empty(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/study/domain-corrections", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandler_DomainCorrections_MinHitCountFilterAndFields(t *testing.T) {
	handler, svc := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	if _, _, err := svc.store.UpsertDomainCorrection("terms-a", "问题A", []string{"d2"}, []string{"d1"}, "tr-a"); err != nil {
		t.Fatalf("seed correction a: %v", err)
	}
	if _, _, err := svc.store.UpsertDomainCorrection("terms-b", "问题B", []string{"d3"}, []string{"d1", "d2"}, "tr-b"); err != nil {
		t.Fatalf("seed correction b: %v", err)
	}
	if _, _, err := svc.store.UpsertDomainCorrection("terms-b", "问题B更新", []string{"d3"}, []string{"d1", "d2"}, "tr-b2"); err != nil {
		t.Fatalf("seed correction b 2nd hit: %v", err)
	}

	req := httptest.NewRequest("GET", "/study/domain-corrections?min_hit_count=2", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var corrections []DomainCorrectionEntry
	if err := json.Unmarshal(w.Body.Bytes(), &corrections); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(corrections) != 1 {
		t.Fatalf("expected 1 correction with hit_count>=2, got %d", len(corrections))
	}
	c := corrections[0]
	if c.QuestionTerms != "terms-b" {
		t.Errorf("expected terms-b, got %s", c.QuestionTerms)
	}
	if c.HitCount != 2 {
		t.Errorf("expected hit_count=2, got %d", c.HitCount)
	}
	if len(c.AttemptedDomainIDs) != 1 || c.AttemptedDomainIDs[0] != "d3" {
		t.Errorf("expected attempted_domain_ids=[d3], got %v", c.AttemptedDomainIDs)
	}
	if len(c.ResolvedDomainIDs) != 2 {
		t.Errorf("expected 2 resolved_domain_ids, got %v", c.ResolvedDomainIDs)
	}
	if c.LastTraceID != "tr-b2" {
		t.Errorf("expected last_trace_id=tr-b2, got %s", c.LastTraceID)
	}
	if c.Recommendation == "" {
		t.Error("expected non-empty recommendation")
	}
}
