package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Shared by diag_test.go and edge_test.go. integration_test.go lives in the
// separate session_test package (to avoid an import cycle through
// retrieval, see its file comment) and keeps its own copies of these.

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find project root")
		}
		dir = parent
	}
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}
