package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFileViewClientConvertToMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/convert/markdown":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"taskId": "task-123",
					"status": "processing",
				},
			})
		case r.Method == "GET" && r.URL.Path == "/api/task/task-123":
			// Task API: top-level fields (matching real FileView)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"taskId":      "task-123",
				"status":      "finished",
				"markdownUrl": "http://" + r.Host + "/download/task-123.md",
			})
		case r.Method == "GET" && r.URL.Path == "/download/task-123.md":
			w.Write([]byte("# Hello\n\nConverted content"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpFile := filepath.Join(t.TempDir(), "test.pdf")
	os.WriteFile(tmpFile, []byte("fake pdf content"), 0644)

	client := NewFileViewClient(server.URL, 50, 10)
	md, err := client.ConvertToMarkdown(context.Background(), tmpFile)
	if err != nil {
		t.Fatalf("ConvertToMarkdown: %v", err)
	}
	if string(md) != "# Hello\n\nConverted content" {
		t.Errorf("got %q", string(md))
	}
}

func TestFileViewClientConvertFinishedImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/convert/markdown":
			// Small file: finished immediately
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"taskId": "fast-task",
					"status": "finished",
				},
			})
		case r.Method == "GET" && r.URL.Path == "/api/task/fast-task":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"taskId":      "fast-task",
				"status":      "finished",
				"markdownUrl": "http://" + r.Host + "/download/fast.md",
			})
		case r.Method == "GET" && r.URL.Path == "/download/fast.md":
			w.Write([]byte("# Fast result"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpFile := filepath.Join(t.TempDir(), "test.md")
	os.WriteFile(tmpFile, []byte("# Source"), 0644)

	client := NewFileViewClient(server.URL, 50, 10)
	md, err := client.ConvertToMarkdown(context.Background(), tmpFile)
	if err != nil {
		t.Fatalf("ConvertToMarkdown: %v", err)
	}
	if string(md) != "# Fast result" {
		t.Errorf("got %q", string(md))
	}
}

func TestFileViewClientTaskFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/convert/markdown":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{"taskId": "fail-task", "status": "processing"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/task/fail-task":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"taskId": "fail-task",
				"status": "failed",
				"error":  "unsupported format",
			})
		}
	}))
	defer server.Close()

	tmpFile := filepath.Join(t.TempDir(), "test.xyz")
	os.WriteFile(tmpFile, []byte("content"), 0644)

	client := NewFileViewClient(server.URL, 50, 10)
	_, err := client.ConvertToMarkdown(context.Background(), tmpFile)
	if err == nil {
		t.Fatal("expected error")
	}
}
