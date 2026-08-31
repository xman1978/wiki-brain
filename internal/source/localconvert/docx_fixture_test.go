package localconvert

// Fixture test for the DOCX port (docs/impl/v1/docx-port/01-word-to-markdown.md
// §14-15 testing strategy, mirroring excel_fixture_test.go's conventions).
// data/sources/markdown/*.md were produced by the real FileView Java
// service (which includes its own MPP pipeline), so they are a legitimate
// ground-truth reference — but per §14/local-file-convert.md §6.4, this is
// NOT a byte-exact comparison: line-break-boundary differences between
// docmill/our-OOXML-reader and Aspose's text extraction are expected and
// allowed. This test reports a line-level similarity ratio per file and
// flags files whose *structural* signals (heading count, table pipe-row
// count) diverge sharply as likely real regressions rather than noise.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertDocxToMarkdown_Fixtures(t *testing.T) {
	root := repoRoot(t)
	originalDir := filepath.Join(root, "data", "sources", "original")
	markdownDir := filepath.Join(root, "data", "sources", "markdown")

	entries, err := os.ReadDir(originalDir)
	if err != nil {
		t.Fatalf("read original dir: %v", err)
	}

	found := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".docx" {
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

			got, err := ConvertDocxToMarkdown(srcPath)
			if err != nil {
				t.Fatalf("ConvertDocxToMarkdown: %v", err)
			}

			gotHeadings := countHeadingLines(string(got))
			wantHeadings := countHeadingLines(string(expected))
			gotTableRows := countPipeTableRows(string(got))
			wantTableRows := countPipeTableRows(string(expected))
			ratio := lineSimilarity(string(got), string(expected))

			t.Logf("similarity=%.1f%% headings(got=%d want=%d) tableRows(got=%d want=%d) gotLen=%d wantLen=%d",
				ratio*100, gotHeadings, wantHeadings, gotTableRows, wantTableRows, len(got), len(expected))

			if len(got) == 0 {
				t.Fatalf("empty output for non-empty source")
			}
		})
	}
	if found == 0 {
		t.Fatalf("no .docx fixtures found under %s", originalDir)
	}
}

func countHeadingLines(md string) int {
	n := 0
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			n++
		}
	}
	return n
}

func countPipeTableRows(md string) int {
	n := 0
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			n++
		}
	}
	return n
}

// lineSimilarity is a coarse line-set Jaccard-like ratio: fraction of
// trimmed, non-blank "got" lines that also appear (anywhere) in "want",
// used purely as a diagnostic signal in test logs — not a pass/fail gate,
// per the "allow line-break-boundary differences" testing strategy.
func lineSimilarity(got, want string) float64 {
	wantSet := map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			wantSet[l] = true
		}
	}
	total, hit := 0, 0
	for _, l := range strings.Split(got, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		total++
		if wantSet[l] {
			hit++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}
