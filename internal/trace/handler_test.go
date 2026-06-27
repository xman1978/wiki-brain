package trace

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupHandler(t *testing.T) (*Handler, *Store, *sql.DB) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	svc := NewService(store)
	handler := NewHandler(svc)
	return handler, store, db
}

func TestHandler_ListTraces(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestAnswer(t, db, "a-1")
	store.SaveTrace(&Trace{
		TraceID: "t-1", AnswerID: "a-1", Question: "q",
		QuestionHash: "h", QuestionTerms: "t",
		RetrievalQuality: QualityConfident, Path: "short",
		DirectPointIDs: []string{},
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/traces", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	var traces []Trace
	json.Unmarshal(w.Body.Bytes(), &traces)
	if len(traces) != 1 {
		t.Errorf("expected 1 trace, got %d", len(traces))
	}
}

func TestHandler_GetTrace(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestAnswer(t, db, "a-1")
	store.SaveTrace(&Trace{
		TraceID: "t-get", AnswerID: "a-1", Question: "q",
		QuestionHash: "h", QuestionTerms: "t",
		RetrievalQuality: QualityGap, Path: "none",
		DirectPointIDs: []string{},
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/traces/t-get", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	var tr Trace
	json.Unmarshal(w.Body.Bytes(), &tr)
	if tr.TraceID != "t-get" {
		t.Errorf("trace_id: got %q", tr.TraceID)
	}
}

func TestHandler_GetTrace_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/traces/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_PostFeedback(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestAnswer(t, db, "a-1")
	store.SaveTrace(&Trace{
		TraceID: "t-fb", AnswerID: "a-1", Question: "q",
		QuestionHash: "h", QuestionTerms: "t",
		RetrievalQuality: QualityConfident, Path: "short",
		DirectPointIDs: []string{},
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body, _ := json.Marshal(FeedbackRequest{Type: "correction", Content: "fix"})
	req := httptest.NewRequest("POST", "/traces/t-fb/feedback", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestHandler_PostFeedback_InvalidType(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestAnswer(t, db, "a-1")
	store.SaveTrace(&Trace{
		TraceID: "t-fb2", AnswerID: "a-1", Question: "q",
		QuestionHash: "h", QuestionTerms: "t",
		RetrievalQuality: QualityConfident, Path: "short",
		DirectPointIDs: []string{},
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body, _ := json.Marshal(FeedbackRequest{Type: "invalid"})
	req := httptest.NewRequest("POST", "/traces/t-fb2/feedback", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_ListCooccurrence(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestKP(t, db, "p1")
	store.UpdateCooccurrence("h1", "terms", []string{"p1"}, QualityConfident)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/cooccurrence?point_id=p1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	var coocs []Cooccurrence
	json.Unmarshal(w.Body.Bytes(), &coocs)
	if len(coocs) != 1 {
		t.Errorf("expected 1 cooccurrence, got %d", len(coocs))
	}
}

func TestHandler_ListLearningEvents(t *testing.T) {
	h, store, db := setupHandler(t)
	insertTestAnswer(t, db, "a-1")
	store.SaveTrace(&Trace{
		TraceID: "t-ev", AnswerID: "a-1", Question: "q",
		QuestionHash: "h", QuestionTerms: "t",
		RetrievalQuality: QualityGap, Path: "none",
		DirectPointIDs: []string{},
	})
	store.SaveLearningEvent("t-ev", "knowledge_gap", `{"question":"q"}`)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/learning-events?type=knowledge_gap&processed=0", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	var events []LearningEvent
	json.Unmarshal(w.Body.Bytes(), &events)
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}
