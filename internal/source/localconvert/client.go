package localconvert

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
)

// LocalConvertClient is a pure-Go, in-process fallback for the remote
// FileView conversion service (docs/impl/v1/local-file-convert.md). It
// implements the same method shape as source.FileViewClient by structural
// typing, so it can be swapped in via config without changing any consumer.
//
// xlsx (docs/impl/v1/xlsx-port/01-excel-to-markdown.md), docx
// (docs/impl/v1/docx-port/01-word-to-markdown.md), pdf
// (docs/impl/v1/pdf-port/), pptx (docs/impl/v1/pptx-port/) and
// md/markdown/txt (text.go, charset-normalize passthrough) are implemented.
// Legacy .xls/.doc/.ppt (binary OLE2 formats) and .ofd return an explicit
// unsupported error here — never a silent empty conversion.
type LocalConvertClient struct{}

func NewLocalConvertClient() *LocalConvertClient {
	return &LocalConvertClient{}
}

func (c *LocalConvertClient) ConvertToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(srcPath)) {
	case ".xlsx":
		return ConvertExcelToMarkdown(srcPath)
	case ".xls":
		return nil, fmt.Errorf("localconvert: unsupported format .xls (legacy binary Excel is not covered, only .xlsx)")
	case ".docx":
		return ConvertDocxToMarkdown(srcPath)
	case ".doc":
		return nil, fmt.Errorf("localconvert: unsupported format .doc (legacy binary Word is not covered, only .docx OOXML)")
	case ".pdf":
		return ConvertPDFToMarkdown(ctx, srcPath)
	case ".ofd":
		return nil, fmt.Errorf("localconvert: unsupported format .ofd (OFD is not a PDF variant and is not covered)")
	case ".pptx":
		return ConvertPptxToMarkdown(srcPath)
	case ".ppt":
		return nil, fmt.Errorf("localconvert: unsupported format .ppt (legacy binary PowerPoint is not covered, only .pptx OOXML)")
	case ".md", ".markdown", ".txt":
		return ConvertTextToMarkdown(srcPath)
	default:
		return nil, fmt.Errorf("localconvert: unsupported format %s (only .xlsx, .docx, .pdf and .pptx are implemented)", filepath.Ext(srcPath))
	}
}

// ConvertToHTML renders a basic preview from the already-converted Markdown
// (docs/impl/v1/local-file-convert.md §7) rather than reparsing the source.
func (c *LocalConvertClient) ConvertToHTML(ctx context.Context, srcPath string) ([]byte, error) {
	md, err := c.ConvertToMarkdown(ctx, srcPath)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := goldmark.Convert(md, &buf); err != nil {
		return nil, fmt.Errorf("localconvert: render html preview: %w", err)
	}
	return buf.Bytes(), nil
}
