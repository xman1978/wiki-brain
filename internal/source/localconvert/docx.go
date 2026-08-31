package localconvert

// Ports docs/impl/v1/docx-port/01-word-to-markdown.md end to end: §1 (top
// level flow — the DOCX-specific preprocessing steps 1-5 are handled by the
// raw-XML reader in docx_xml.go, since track-changes/field-removal are
// resolved during parsing rather than as separate document mutation
// passes), §2 (collectBodyBlocks), §5 (renderBodyBlocks), §9 (cleanOutput),
// and §12 (MPP post-processing pipeline reuse).

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

var (
	bellRunRe        = regexp.MustCompile("\x07+")
	otherControlRe   = regexp.MustCompile(`[\x00-\x06\x08\x0E-\x1F]`)
	leadingBlankLinesRe = regexp.MustCompile(`^\n+`)
)

// cleanOutput ports docx-port/01 §9 WordToMarkdown.cleanOutput.
func cleanOutput(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	out := strings.ReplaceAll(text, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = strings.ReplaceAll(out, "\f", "")
	out = strings.ReplaceAll(out, "\v", "") // residual soft breaks not consumed by expandSoftBreakPlainLines
	out = bellRunRe.ReplaceAllString(out, "\n")
	out = otherControlRe.ReplaceAllString(out, "")
	out = leadingBlankLinesRe.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// ConvertDocxToMarkdown converts a .docx file to Markdown, following
// WordToMarkdown.java's algorithm (docs/impl/v1/docx-port/01-word-to-markdown.md)
// rather than any built-in third-party Markdown exporter — see docx_xml.go's
// header comment for why a raw OOXML reader is used instead of a library.
func ConvertDocxToMarkdown(srcPath string) ([]byte, error) {
	doc, err := parseDocx(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: open docx: %w", err)
	}

	blocks := collectBodyBlocks(doc)
	raw := renderBodyBlocks(blocks)
	cleaned := cleanOutput(raw)

	// docs/impl/v1/docx-port/01-word-to-markdown.md §12: Word's raw output
	// must go through the same MPP post-processing pipeline PDF conversion
	// uses (ConvertWorker.java:434), not be treated as final on its own —
	// except stage 3 (mergeWrappedBodyLines): DOCX paragraph boundaries come
	// from the OOXML tree and are authoritative, unlike PDF text where a
	// "line" is only a page-width reflow artifact (see
	// RunMarkdownPipelineNoBodyMerge's doc comment).
	final := pdfconv.RunMarkdownPipelineNoBodyMerge(cleaned)
	return []byte(final), nil
}
