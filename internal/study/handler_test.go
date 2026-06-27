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
		CandidateConfidentMin: 5,
		CandidateRatioMin:     0.6,
		WikiKPMin:             4,
		WikiConfidentMin:      8,
		GapHitThreshold:       3,
		ScanBatchSize:         200,
		ReportPeriodDays:      30,
		ReportMaxKeep:         10,
	}
	svc := NewService(store, cfg)
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
