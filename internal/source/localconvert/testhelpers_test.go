package localconvert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoRoot walks up from the current working directory to find the module
// root (marked by go.mod), for tests that read fixture files by repo-relative
// path.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (go.mod not found)")
		}
		dir = parent
	}
}

// extractFirstFence returns the content of the first ```<lang> ... ``` fence
// in content, or "" if none is found.
func extractFirstFence(content, lang string) string {
	open := "```" + lang
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == open {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := -1
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	if end == -1 {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// assertJSONStructurallyEqual parses both JSON strings and compares them
// ignoring object key order (map key order is insignificant in JSON) while
// still requiring array order and values to match exactly.
func assertJSONStructurallyEqual(t *testing.T, want, got string) {
	t.Helper()
	var wantVal, gotVal interface{}
	if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
		t.Fatalf("parse expected json: %v\n%s", err, want)
	}
	if err := json.Unmarshal([]byte(got), &gotVal); err != nil {
		t.Fatalf("parse actual json: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(wantVal, gotVal) {
		t.Errorf("json structural mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
