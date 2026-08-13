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
	svc := newTestService(store, NewMatcher(store))
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
	seedKPFull(t, db, "kp2")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "s1"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)
	// Different point_id — a point has at most one link (idx_al_point_id is
	// UNIQUE), so a second candidate to filter out must target a different KP.
	if _, err := svc.CreateLink("t2", LinkCondition{}, "kp2", nil); err != nil {
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

func TestHandler_Get_OmitsLearningResults(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")

	l, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "s1"}, "kp1", []string{"ev1"})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	verifyLink(t, svc, l)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/activation-links/"+l.LinkID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["learning_results"]; ok {
		t.Error("GET detail must not embed learning_results (lazy endpoint)")
	}
	if resp["created_from"] == nil {
		t.Error("expected created_from to be present")
	}
	conditions, ok := resp["conditions"].([]interface{})
	if !ok || len(conditions) != 1 {
		t.Fatalf("expected 1 condition in conditions[], got %+v", resp["conditions"])
	}
	first, _ := conditions[0].(map[string]interface{})
	if first["tier"] != string(TierSelfGraded) && first["tier"] != string(TierTrusted) {
		t.Errorf("tier = %v, want self_graded or trusted after verifyLink boost", first["tier"])
	}

	req = httptest.NewRequest("GET", "/activation-links/"+l.LinkID+"/learning-results", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("learning-results status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestHandler_Reject(t *testing.T) {
	handler, svc := setupHandler(t)
	db := setupDBFromSvc(t, svc)
	seedKPFull(t, db, "kp1")

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	l1, err := svc.CreateLink("t1", LinkCondition{SubjectTerms: "s1"}, "kp1", nil)
	if err != nil {
		t.Fatalf("create link1: %v", err)
	}
	verifyLink(t, svc, l1)

	req := httptest.NewRequest("POST", "/activation-links/"+l1.LinkID+"/reject", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", w.Code, w.Body.String())
	}
	var rejectResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &rejectResp)
	if rejectResp["status"] != StatusCandidate {
		t.Errorf("reject response status = %q, want candidate (cleared conditions default landing point)", rejectResp["status"])
	}

	// Reject is valid for any status — rejecting again (now candidate) must
	// still succeed, not require a specific prior status.
	req = httptest.NewRequest("POST", "/activation-links/"+l1.LinkID+"/reject", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("re-reject status = %d, want 200 (valid for any status)", w.Code)
	}
}

func TestHandler_Synonyms_ListGetConfirmReject(t *testing.T) {
	handler, svc := setupHandler(t)

	created, err := svc.CreateSynonymCandidate("", "证券市场", "股票市场", nil)
	if err != nil {
		t.Fatalf("create synonym candidate: %v", err)
	}

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// List
	req := httptest.NewRequest("GET", "/subject-synonyms?status=candidate", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", w.Code, w.Body.String())
	}
	var listResp []synonymResp
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listResp) != 1 || listResp[0].Term != "证券市场" || listResp[0].Canonical != "股票市场" {
		t.Fatalf("unexpected list response: %+v", listResp)
	}

	// Get
	req = httptest.NewRequest("GET", "/subject-synonyms/"+created.SynonymID, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", w.Code, w.Body.String())
	}

	// Get missing
	req = httptest.NewRequest("GET", "/subject-synonyms/does-not-exist", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing status = %d, want 404", w.Code)
	}

	// Confirm
	req = httptest.NewRequest("POST", "/subject-synonyms/"+created.SynonymID+"/confirm", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", w.Code, w.Body.String())
	}
	var confirmResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &confirmResp)
	if confirmResp["status"] != SynonymStatusActive {
		t.Errorf("confirm response status = %q, want active", confirmResp["status"])
	}

	// Re-confirming an already-active synonym must fail.
	req = httptest.NewRequest("POST", "/subject-synonyms/"+created.SynonymID+"/confirm", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("re-confirm status = %d, want 400", w.Code)
	}

	// Reject a second candidate.
	created2, err := svc.CreateSynonymCandidate("", "二级市场", "股票市场", nil)
	if err != nil {
		t.Fatalf("create synonym candidate 2: %v", err)
	}
	req = httptest.NewRequest("POST", "/subject-synonyms/"+created2.SynonymID+"/reject", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", w.Code, w.Body.String())
	}
	var rejectResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &rejectResp)
	if rejectResp["status"] != SynonymStatusRejected {
		t.Errorf("reject response status = %q, want rejected", rejectResp["status"])
	}
}

func setupDBFromSvc(t *testing.T, svc *Service) *sql.DB {
	t.Helper()
	return svc.Store().db
}
