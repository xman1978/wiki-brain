package source

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func TestHandlerCreateSource(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create a multipart form with a file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, _ := writer.CreateFormFile("file", "test.md")
	part.Write([]byte("# Hello\n\nWorld"))
	writer.Close()

	req := httptest.NewRequest("POST", "/sources", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["source_id"] == nil || resp["source_id"] == "" {
		t.Error("missing source_id")
	}
	if resp["format"] != "markdown" {
		t.Errorf("format = %v, want markdown", resp["format"])
	}

	// Verify file was saved
	sourceID := resp["source_id"].(string)
	origPath := filepath.Join(svc.baseDir, "data", "sources", "original", sourceID+".md")
	if _, err := os.Stat(origPath); err != nil {
		t.Errorf("original file not created: %v", err)
	}
}

func TestHandlerCreateSource_UnsupportedFormat(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, _ := writer.CreateFormFile("file", "test.xyz")
	part.Write([]byte("content"))
	writer.Close()

	req := httptest.NewRequest("POST", "/sources", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandlerListSources(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create some sources
	for _, name := range []string{"a.md", "b.md"} {
		svc.store.Create(&Source{
			Title: name, Format: "markdown", FileName: name,
			OriginalPath: "o/" + name, MarkdownPath: "m/" + name, Status: "pending",
		})
	}

	req := httptest.NewRequest("GET", "/sources", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Items) != 2 {
		t.Errorf("len = %d, want 2", len(resp.Items))
	}
}

func TestHandlerGetSource(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	src := &Source{
		SourceID: "test-id", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "completed",
	}
	svc.store.Create(src)

	req := httptest.NewRequest("GET", "/sources/test-id", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["title"] != "Test" {
		t.Errorf("title = %v", resp["title"])
	}
}

func TestHandlerGetOutlines(t *testing.T) {
	svc, fake := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "summary"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	sourceID := "outline-test"
	content := "# H1\n\n## S1\n\ntext\n\n## S2\n\ntext\n\n## S3\n\ntext\n\n## S4\n\ntext\n\n## S5\n\ntext"
	mdDir := filepath.Join(svc.baseDir, "data", "sources", "markdown")
	os.WriteFile(filepath.Join(mdDir, sourceID+".md"), []byte(content), 0644)
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".md"), []byte(content), 0644)

	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Outline Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "data/sources/original/" + sourceID + ".md",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md", Status: "pending",
	})
	svc.Process(nil, sourceID)

	req := httptest.NewRequest("GET", "/sources/"+sourceID+"/outlines", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var tree []OutlineTree
	json.NewDecoder(rr.Body).Decode(&tree)
	if len(tree) == 0 {
		t.Error("expected outline tree")
	}
}

func TestHandlerGetMarkdown(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	sourceID := "md-test"
	mdDir := filepath.Join(svc.baseDir, "data", "sources", "markdown")
	os.WriteFile(filepath.Join(mdDir, sourceID+".md"), []byte("# Hello"), 0644)

	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "data/sources/markdown/" + sourceID + ".md",
		Status: "completed",
	})

	req := httptest.NewRequest("GET", "/sources/"+sourceID+"/markdown", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "# Hello" {
		t.Errorf("body = %q", string(body))
	}
}

func TestHandlerRetrySource(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.store.Create(&Source{
		SourceID: "retry-test", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "completed",
	})

	// Retry on non-failed should return 400
	req := httptest.NewRequest("POST", "/sources/retry-test/retry", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}

	// Set to failed and retry
	errMsg := "some error"
	svc.store.UpdateStatus("retry-test", "failed", &errMsg)

	req = httptest.NewRequest("POST", "/sources/retry-test/retry", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandlerDeleteSource_CompletedRejected(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.store.Create(&Source{
		SourceID: "del-complete", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "completed",
	})

	req := httptest.NewRequest("DELETE", "/sources/del-complete", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (completed sources cannot be deleted)", rr.Code)
	}
}

func TestHandlerDeleteSource_Failed(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create a failed source (only original file exists, no markdown)
	sourceID := "del-failed"
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".pdf"), []byte("fake pdf"), 0644)

	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Failed Doc", Format: "pdf", FileName: "test.pdf",
		OriginalPath: "data/sources/original/" + sourceID + ".pdf",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md", Status: "failed",
		ErrorMsg: sql.NullString{String: "conversion failed", Valid: true},
	})

	req := httptest.NewRequest("DELETE", "/sources/"+sourceID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rr.Code)
	}

	// Verify cleanup
	_, err := svc.store.GetByID(sourceID)
	if err == nil {
		t.Error("failed source should be deleted from DB")
	}
	if _, err := os.Stat(filepath.Join(origDir, sourceID+".pdf")); !os.IsNotExist(err) {
		t.Error("original file should be deleted")
	}
}

func TestHandlerDeleteSource_NotFound(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("DELETE", "/sources/nonexistent", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
