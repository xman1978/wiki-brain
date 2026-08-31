package localconvert

// Smoke test for the ConvertPptxToMarkdown entry point (docs/impl/v1/pptx-port/
// 01-pptx-to-markdown.md §7). Unlike DOCX/XLSX, there is no FileView reference
// output to diff against (FileView has no PPT-to-Markdown pipeline) — the
// acceptance bar here is "converts without error, output looks structured",
// per the design doc. The bulk of behavioral coverage lives in the inlined
// internal/source/localconvert/pptx package's own tests.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertPptxToMarkdown_Fixture(t *testing.T) {
	root := repoRoot(t)
	srcPath := filepath.Join(root, "internal", "source", "localconvert", "pptx", "testdata", "fixtures", "basic.pptx")

	got, err := ConvertPptxToMarkdown(srcPath)
	if err != nil {
		t.Fatalf("ConvertPptxToMarkdown: %v", err)
	}
	md := string(got)
	if !strings.HasPrefix(md, "# ") {
		t.Fatalf("expected markdown to start with a title heading, got:\n%s", md)
	}
	if !strings.Contains(md, "## Slide 1:") {
		t.Fatalf("expected a slide heading, got:\n%s", md)
	}
}
