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
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
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

func TestHandlerListSources_FilterByFileName(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	for _, name := range []string{"alpha-spec.md", "beta-notes.md", "alpha-plan.pdf"} {
		svc.store.Create(&Source{
			Title: name, Format: "markdown", FileName: name,
			OriginalPath: "o/" + name, MarkdownPath: "m/" + name, Status: "pending",
		})
	}

	req := httptest.NewRequest("GET", "/sources?q=alpha", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	var resp struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("len = %d, want 2", len(resp.Items))
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

func TestHandlerSourceVersions(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	sourceID := "ver-test"
	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Test", Format: "markdown", FileName: "current.md",
		OriginalPath: "data/sources/original/" + sourceID + ".md",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md",
		Status:       "completed",
	})

	archiveDir := filepath.Join(svc.baseDir, "data", "sources", "archived", sourceID, "20260101T000000Z")
	os.MkdirAll(archiveDir, 0755)
	archivedRel := filepath.Join("data", "sources", "archived", sourceID, "20260101T000000Z", "old.md")
	os.WriteFile(filepath.Join(svc.baseDir, archivedRel), []byte("旧版内容"), 0644)

	if err := svc.store.InsertSourceVersion(&SourceVersion{
		SourceID: sourceID, Version: 1, FileName: "old.md",
		OriginalPath: archivedRel, MarkdownPath: archivedRel,
	}); err != nil {
		t.Fatalf("InsertSourceVersion: %v", err)
	}

	t.Run("list", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/sources/"+sourceID+"/versions", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
		}
		var items []map[string]interface{}
		json.NewDecoder(rr.Body).Decode(&items)
		if len(items) != 1 {
			t.Fatalf("got %d items, want 1", len(items))
		}
		if items[0]["version"] != float64(1) || items[0]["file_name"] != "old.md" {
			t.Errorf("item = %+v", items[0])
		}
	})

	t.Run("download", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/sources/"+sourceID+"/versions/1/download", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != "旧版内容" {
			t.Errorf("body = %q", got)
		}
		if !bytes.Contains([]byte(rr.Header().Get("Content-Disposition")), []byte("old.md")) {
			t.Errorf("Content-Disposition = %q, want filename old.md", rr.Header().Get("Content-Disposition"))
		}
	})

	t.Run("preview", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/sources/"+sourceID+"/versions/1/preview", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != "<pre>旧版内容</pre>" {
			t.Errorf("body = %q", got)
		}
	})

	t.Run("nonexistent version 404s", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/sources/"+sourceID+"/versions/99/download", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
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

// TestHandlerDeleteSource_CompletedSoftDeletes verifies the lifecycle module's
// dispatch rule (docs/impl/v1/lifecycle.md 步骤 2): completed (non-failed)
// sources are soft-deleted (200 + deprecated_units), not hard-deleted.
func TestHandlerDeleteSource_CompletedSoftDeletes(t *testing.T) {
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
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (soft delete)", rr.Code)
	}

	got, err := svc.store.GetByID("del-complete")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "deleted" {
		t.Errorf("status = %q, want deleted", got.Status)
	}
}

// TestHandlerDeleteSource_AlreadyDeletedRejected verifies re-deleting an
// already soft-deleted source is rejected rather than silently re-processed.
func TestHandlerDeleteSource_AlreadyDeletedRejected(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.store.Create(&Source{
		SourceID: "del-already", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "deleted",
	})

	req := httptest.NewRequest("DELETE", "/sources/del-already", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (already deleted)", rr.Code)
	}
}

// TestHandlerRestoreSource_DeletedFlipsBackToCompleted covers 文件管理 恢复按钮:
// POST /sources/:id/restore on a soft-deleted source flips it back to completed.
func TestHandlerRestoreSource_DeletedFlipsBackToCompleted(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.store.Create(&Source{
		SourceID: "restore-1", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "deleted",
	})

	req := httptest.NewRequest("POST", "/sources/restore-1/restore", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rr.Code, rr.Body.String())
	}

	got, err := svc.store.GetByID("restore-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
}

// TestHandlerRestoreSource_NonDeletedRejected verifies restoring a source
// that isn't currently soft-deleted is rejected.
func TestHandlerRestoreSource_NonDeletedRejected(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.store.Create(&Source{
		SourceID: "restore-2", Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "completed",
	})

	req := httptest.NewRequest("POST", "/sources/restore-2/restore", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not deleted)", rr.Code)
	}
}

func TestHandlerRestoreSource_NotFound(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/sources/nonexistent/restore", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
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

// TestHandlerDeleteSource_UnitsFailedHardDeletes covers a source whose parse
// succeeded (status=completed) but whose knowledge-unit extraction failed
// (units_status=failed): it must be treated as failed and hard-deleted, not
// silently soft-deleted (which would leave the failed row/files behind with
// no way to retry via reupload's filename dedup check).
func TestHandlerDeleteSource_UnitsFailedHardDeletes(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	sourceID := "del-units-failed"
	origDir := filepath.Join(svc.baseDir, "data", "sources", "original")
	os.WriteFile(filepath.Join(origDir, sourceID+".pdf"), []byte("fake pdf"), 0644)

	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Test", Format: "pdf", FileName: "test.pdf",
		OriginalPath: "data/sources/original/" + sourceID + ".pdf",
		MarkdownPath: "data/sources/markdown/" + sourceID + ".md", Status: "completed",
	})
	if err := svc.store.UpdateUnitsStatus(sourceID, "failed"); err != nil {
		t.Fatalf("UpdateUnitsStatus: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/sources/"+sourceID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (hard delete), body: %s", rr.Code, rr.Body.String())
	}

	if _, err := svc.store.GetByID(sourceID); err == nil {
		t.Error("units-failed source should be deleted from DB")
	}
	if _, err := os.Stat(filepath.Join(origDir, sourceID+".pdf")); !os.IsNotExist(err) {
		t.Error("original file should be deleted")
	}
}

func TestHandlerRetrySource_UnitsFailedReenqueuesExtractOnly(t *testing.T) {
	svc, _ := setupTestService(t)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	sourceID := "retry-units-failed"
	svc.store.Create(&Source{
		SourceID: sourceID, Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/test.md", MarkdownPath: "m/test.md", Status: "completed",
	})
	if err := svc.store.UpdateUnitsStatus(sourceID, "failed"); err != nil {
		t.Fatalf("UpdateUnitsStatus: %v", err)
	}

	enqueued := make(chan queue.UnitTask, 1)
	svc.queue.RegisterHandler(queue.TaskTypeUnitExtract, func(payload interface{}) {
		enqueued <- payload.(queue.UnitTask)
	})
	svc.queue.RegisterHandler(queue.TaskTypeSourceProcess, func(payload interface{}) {
		t.Errorf("unexpected source_process re-enqueue for units_status=failed retry: %#v", payload)
	})
	svc.queue.StartN(1)
	t.Cleanup(svc.queue.Shutdown)

	req := httptest.NewRequest("POST", "/sources/"+sourceID+"/retry", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	select {
	case ut := <-enqueued:
		if ut.SourceID != sourceID {
			t.Errorf("enqueued unit_extract source_id = %q, want %q", ut.SourceID, sourceID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for unit_extract to be enqueued")
	}

	// status (parse stage) is untouched — only unit_extract should be
	// re-enqueued, never the whole source_process pipeline.
	got, err := svc.store.GetByID(sourceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want unchanged completed", got.Status)
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
