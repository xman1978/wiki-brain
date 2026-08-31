package localconvert

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConvertExcelToMarkdown_Fixtures runs the real sample xlsx files under
// data/sources/original against the production markdown captured under
// data/sources/markdown (see docs/impl/v1/local-file-convert.md §4.4). The
// expected files are raw snapshots of sources.content at different points in
// time: some were captured before the stripPivotTextDuplicate normalize.go
// pass existed (still have the ```text block) and some after (json only) —
// ConvertToMarkdown itself must always emit the pre-normalize (json+text)
// shape per xlsx-port/01 §1, so this test compares the JSON block structurally
// and only checks the text block when the fixture still has one.
func TestConvertExcelToMarkdown_Fixtures(t *testing.T) {
	root := repoRoot(t)
	originalDir := filepath.Join(root, "data", "sources", "original")
	markdownDir := filepath.Join(root, "data", "sources", "markdown")

	entries, err := os.ReadDir(originalDir)
	if err != nil {
		t.Fatalf("read original dir: %v", err)
	}

	found := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".xlsx" {
			continue
		}
		found++
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			srcPath := filepath.Join(originalDir, name)
			expectedPath := filepath.Join(markdownDir, name[:len(name)-len(filepath.Ext(name))]+".md")

			expected, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected markdown: %v", err)
			}

			got, err := ConvertExcelToMarkdown(srcPath)
			if err != nil {
				t.Fatalf("ConvertExcelToMarkdown: %v", err)
			}

			gotJSON := extractFirstFence(string(got), "json")
			wantJSON := extractFirstFence(string(expected), "json")
			if gotJSON == "" || wantJSON == "" {
				t.Fatalf("could not extract json fence (got=%d bytes, want=%d bytes)", len(gotJSON), len(wantJSON))
			}
			assertJSONStructurallyEqual(t, wantJSON, gotJSON)

			if wantText := extractFirstFence(string(expected), "text"); wantText != "" {
				gotText := extractFirstFence(string(got), "text")
				if gotText != wantText {
					t.Errorf("text fence mismatch\n--- want ---\n%s\n--- got ---\n%s", wantText, gotText)
				}
			}
		})
	}
	if found == 0 {
		t.Fatalf("no .xlsx fixtures found under %s", originalDir)
	}
}

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
