package concept

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupHandler(t *testing.T) (*Handler, *Store, *sql.DB) {
	t.Helper()
	svc, store, db := setupService(t)
	return NewHandler(svc), store, db
}

func TestHandler_ListCandidates(t *testing.T) {
	h, store, db := setupHandler(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	if _, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{EventCount: 5}, "seed"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/concepts/candidates?status=pending_confirm", nil)
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var views []CandidateView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(views))
	}
	if views[0].SuggestedName != "topic" {
		t.Errorf("suggested_name = %q, want topic", views[0].SuggestedName)
	}
}

func TestHandler_Confirm_Add(t *testing.T) {
	h, store, db := setupHandler(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(confirmRequestBody{SuggestedName: "并发编程"})
	req := httptest.NewRequest(http.MethodPost, "/concepts/candidates/"+candidateID+"/confirm", bytes.NewReader(body))
	req.SetPathValue("id", candidateID)
	rec := httptest.NewRecorder()
	h.confirm(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp confirmResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ConceptID == "" || resp.MigratedKUs != 1 {
		t.Errorf("unexpected confirm response: %+v", resp)
	}
}

func TestHandler_Confirm_InvalidCandidate_BadRequest(t *testing.T) {
	h, _, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/concepts/candidates/missing/confirm", nil)
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()
	h.confirm(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_ListConcepts_FiltersByDomainAndExcludesMerged(t *testing.T) {
	h, _, db := setupHandler(t)
	seedDomain(t, db, "d2")
	seedConcept(t, db, "c1", "d1", sql.NullString{})
	seedConcept(t, db, "c2", "d2", sql.NullString{})
	seedConcept(t, db, "c3-merged", "d1", sql.NullString{String: "c1", Valid: true})

	req := httptest.NewRequest(http.MethodGet, "/concepts?domain_id=d1", nil)
	rec := httptest.NewRecorder()
	h.listConcepts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var items []struct {
		ConceptID string `json:"concept_id"`
		Name      string `json:"name"`
		DomainID  string `json:"domain_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ConceptID != "c1" {
		t.Fatalf("expected only c1 (d1, unmerged), got %+v", items)
	}
}

func TestHandler_Reject(t *testing.T) {
	h, store, db := setupHandler(t)
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "topic", []string{"p1"}, []string{"evt-1"}, AddEvidence{}, "seed")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/concepts/candidates/"+candidateID+"/reject", nil)
	req.SetPathValue("id", candidateID)
	rec := httptest.NewRecorder()
	h.reject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := store.GetCandidate(candidateID)
	if c.Status != StatusRejected {
		t.Errorf("status = %q, want rejected", c.Status)
	}
}

func TestHandler_ListByDomain(t *testing.T) {
	h, store, db := setupHandler(t)
	seedDomain(t, db, "d1")
	pending, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "pending", nil, nil, AddEvidence{}, "seed")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/concepts/candidates/by-domain?domain_id=d1", nil)
	rec := httptest.NewRecorder()
	h.listByDomain(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var views []CandidateView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].CandidateID != pending {
		t.Fatalf("unexpected views: %+v", views)
	}
}

func TestHandler_ListByDomain_RequiresDomainID(t *testing.T) {
	h, _, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/concepts/candidates/by-domain", nil)
	rec := httptest.NewRecorder()
	h.listByDomain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_ConceptDetailAndEdit(t *testing.T) {
	h, _, db := setupHandler(t)
	seedConcept(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")

	req := httptest.NewRequest(http.MethodGet, "/concepts/c1", nil)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	h.getConceptDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var detail conceptDetailResp
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "c1" || len(detail.Points) != 1 {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	body, _ := json.Marshal(map[string]string{"name": "新名称", "description": "新描述"})
	req2 := httptest.NewRequest(http.MethodPatch, "/concepts/c1", bytes.NewReader(body))
	req2.SetPathValue("id", "c1")
	rec2 := httptest.NewRecorder()
	h.updateConcept(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandler_ConceptDetail_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/concepts/missing", nil)
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()
	h.getConceptDetail(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_AddAndRemoveConceptPoints(t *testing.T) {
	h, _, db := setupHandler(t)
	seedConcept(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")

	body, _ := json.Marshal(map[string][]string{"point_ids": {"p1"}})
	req := httptest.NewRequest(http.MethodPost, "/concepts/c1/points", bytes.NewReader(body))
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()
	h.addConceptPoints(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var addResp map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &addResp); err != nil {
		t.Fatal(err)
	}
	if addResp["migrated"] != 1 {
		t.Fatalf("migrated = %d, want 1", addResp["migrated"])
	}

	req2 := httptest.NewRequest(http.MethodDelete, "/concepts/c1/points/p1", nil)
	req2.SetPathValue("id", "c1")
	req2.SetPathValue("point_id", "p1")
	rec2 := httptest.NewRecorder()
	h.removeConceptPoint(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}
