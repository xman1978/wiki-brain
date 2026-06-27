package retrieval

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestHandlerRetrieve(t *testing.T) {
	svc, fake, _ := setupTestService(t)
	handler := NewHandler(svc)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d1"]}`,
	})
	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": ["s1"]}`,
	})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{
		Output: `{"outline_ids": ["o2"]}`,
	})
	fake.SetResponse("rerank.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct"}]}`,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/retrieval", strings.NewReader(`{"question":"linear equations"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "direct_evidence") {
		t.Errorf("response should contain direct_evidence: %s", body)
	}
}

func TestHandlerRetrieveEmptyQuestion(t *testing.T) {
	svc, _, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/retrieval", strings.NewReader(`{"question":""}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
