package domain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupHandler(t *testing.T) (*Handler, *Store) {
	t.Helper()
	store, _ := setupStore(t)
	return NewHandler(NewService(store)), store
}

func TestHandler_CreateAndList(t *testing.T) {
	h, _ := setupHandler(t)

	body, _ := json.Marshal(map[string]string{"name": "风险管理", "description": "识别、分析、应对与监控"})
	req := httptest.NewRequest(http.MethodPost, "/domains", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.create(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created["domain_id"] == "" {
		t.Fatal("expected non-empty domain_id in response")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/domains", nil)
	rec2 := httptest.NewRecorder()
	h.list(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var domains []Domain
	if err := json.Unmarshal(rec2.Body.Bytes(), &domains); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(domains) != 1 || domains[0].Name != "风险管理" {
		t.Fatalf("unexpected domains: %+v", domains)
	}
}

func TestHandler_Create_RejectsEmptyName(t *testing.T) {
	h, _ := setupHandler(t)

	body, _ := json.Marshal(map[string]string{"name": "   "})
	req := httptest.NewRequest(http.MethodPost, "/domains", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
