package answer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func TestHandler_GetAnswer(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, nil)
	handler := NewHandler(svc)

	r := &AnswerResult{
		AnswerID:    "a-test",
		Question:    "测试",
		Content:     "回答",
		Citations:   []string{},
		HasAnswer:   true,
		Path:        "short",
		EvidenceSet: &retrieval.EvidenceSet{Question: "测试", Path: "short"},
	}
	store.Save(r, "v1", "default")

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/answers/a-test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp AnswerResult
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.AnswerID != "a-test" {
		t.Errorf("expected answer_id=a-test, got %s", resp.AnswerID)
	}
}

func TestHandler_GetAnswer_NotFound(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, nil)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/answers/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_PostAnswerStream_MissingQuestion(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, nil)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/answer/stream", strings.NewReader(`{"question":""}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_PostAnswer_MissingQuestion(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(10)
	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {})
	q.Start()
	t.Cleanup(q.Shutdown)
	svc := NewService(store, fake, q, nil)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/answer", strings.NewReader(`{"question":""}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
