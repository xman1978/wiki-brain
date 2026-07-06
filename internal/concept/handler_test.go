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
