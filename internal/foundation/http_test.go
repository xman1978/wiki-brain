package foundation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["key"] != "value" {
		t.Errorf("body = %v", body)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusBadRequest, "bad input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var body APIError
	json.NewDecoder(w.Body).Decode(&body)
	if body.Code != 400 || body.Message != "bad input" {
		t.Errorf("body = %+v", body)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestID(r.Context())
		if id == "" {
			t.Error("request_id is empty")
		}
		w.Write([]byte(id))
	}))

	// Auto-generated ID
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(w, r)
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("response missing X-Request-ID")
	}

	// Provided ID
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("X-Request-ID", "custom-id")
	handler.ServeHTTP(w2, r2)
	if w2.Header().Get("X-Request-ID") != "custom-id" {
		t.Errorf("request_id = %q, want custom-id", w2.Header().Get("X-Request-ID"))
	}
}

func TestLoggingMiddleware(t *testing.T) {
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/test", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}

func TestChain(t *testing.T) {
	handler := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}),
		RequestIDMiddleware,
		LoggingMiddleware,
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(w, r)

	if w.Body.String() != "ok" {
		t.Errorf("body = %q", w.Body.String())
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID after chain")
	}
}
