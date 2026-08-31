package localconvert

import (
	"context"
	"fmt"
	"os"

	"github.com/ivanvanderbyl/docmill/v2/pkg/parser"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

// ConvertPDFToMarkdown implements docs/impl/v1/local-file-convert.md §6.3:
// parse the PDF with docmill's pure-Go backend, then run the ported
// PdfToMarkdown pipeline (internal/source/localconvert/pdfconv).
func ConvertPDFToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: read pdf: %w", err)
	}
	backend := parser.NewBackend()
	defer backend.Close()

	md, err := pdfconv.ConvertPDFToMarkdown(ctx, data, backend)
	if err != nil {
		return nil, fmt.Errorf("localconvert: convert pdf: %w", err)
	}
	return md, nil
}
