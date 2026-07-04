package activation

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupHandler(t *testing.T) (*Handler, *Service) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)
	svc := NewService(store, NewMatcher(store))
	return NewHandler(svc), svc
}

func TestHandler_List(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")

	if _, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil); err != nil {
		t.Fatalf("create link: %v", err)
	}

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/activation-links", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp []linkResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 link, got %d", len(resp))
	}
	if resp[0].PointSummary != "content of kp1" {
		t.Errorf("point_summary = %q", resp[0].PointSummary)
	}
}

func TestHandler_List_FilterByStatus(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", nil); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := svc.CreateLink("t2", LinkCondition{}, "kp1", nil); err != nil {
		t.Fatalf("create link2: %v", err)
	}

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/activation-links?status=verified", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp []linkResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp) != 1 || resp[0].Status != StatusVerified {
		t.Errorf("expected 1 verified link, got %+v", resp)
	}
}

func TestHandler_Get_NotFound(t *testing.T) {
	handler, _ := setupHandler(t)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/activation-links/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandler_Get_IncludesLearningResults(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{}, "kp1", []string{"ev1"})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := svc.TransitionLink(l.LinkID, StatusVerified, "test", []string{"ev1"}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/activation-links/"+l.LinkID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp linkDetailResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.LearningResults) != 1 {
		t.Errorf("expected 1 learning result, got %d", len(resp.LearningResults))
	}
	if resp.CreatedFrom == nil {
		t.Error("expected created_from to be present (even if empty array)")
	}
}

func TestHandler_ConfirmAndReject(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")
	seedKPFull(t, db, "kp2")

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	l1, err := svc.CreateLink("t1", LinkCondition{}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link1: %v", err)
	}
	req := httptest.NewRequest("POST", "/activation-links/"+l1.LinkID+"/confirm", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", w.Code, w.Body.String())
	}
	var confirmResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &confirmResp)
	if confirmResp["status"] != StatusVerified {
		t.Errorf("confirm response status = %q, want verified", confirmResp["status"])
	}

	l2, err := svc.CreateLink("t2", LinkCondition{}, "kp2", nil)
	if err != nil {
		t.Fatalf("create link2: %v", err)
	}
	req = httptest.NewRequest("POST", "/activation-links/"+l2.LinkID+"/reject", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", w.Code, w.Body.String())
	}
	var rejectResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &rejectResp)
	if rejectResp["status"] != StatusDeprecated {
		t.Errorf("reject response status = %q, want deprecated", rejectResp["status"])
	}

	// Confirming an already-verified link must fail.
	req = httptest.NewRequest("POST", "/activation-links/"+l1.LinkID+"/confirm", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("re-confirm status = %d, want 400", w.Code)
	}
}

func setupDBFromSvc(t *testing.T, svc *Service) *sql.DB {
	t.Helper()
	return svc.Store().db
}
