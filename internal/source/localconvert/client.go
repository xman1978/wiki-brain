package localconvert

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// OCRSettings gates the scanned-PDF/image-file OCR fallback (§10): local
// mode already covers born-digital PDF/DOCX/XLSX/PPTX; this additionally
// enables OCR via the multimodal model bound to the "doc_convert" LLM
// purpose for scanned PDF pages and standalone image files. MaxPages<=0
// falls back to 50. Backed by sysconfig.FileViewSettings.OCREnabled/
// OCRMaxPages, editable from the web page (see docs/impl/v1/local-file-convert.md).
type OCRSettings struct {
	Enabled  bool
	MaxPages int
}

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
//
// Scanned PDF pages and standalone image files (.jpg/.png/.bmp/.tif) go
// through OCR via llmClient/ocrCfg instead (docs/impl/v1/local-file-convert.md
// §10); both fields are optional, in which case scanned PDFs surface
// pdfconv.ErrNoExtractableText as before and image files are unsupported.
type LocalConvertClient struct {
	llmClient llm.LLMClient
	ocrCfg    OCRSettings
}

func NewLocalConvertClient(llmClient llm.LLMClient, ocrCfg OCRSettings) *LocalConvertClient {
	return &LocalConvertClient{llmClient: llmClient, ocrCfg: ocrCfg}
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
		return ConvertPDFToMarkdown(ctx, srcPath, c.llmClient, c.ocrCfg)
	case ".ofd":
		return nil, fmt.Errorf("localconvert: unsupported format .ofd (OFD is not a PDF variant and is not covered)")
	case ".pptx":
		return ConvertPptxToMarkdown(srcPath)
	case ".ppt":
		return nil, fmt.Errorf("localconvert: unsupported format .ppt (legacy binary PowerPoint is not covered, only .pptx OOXML)")
	case ".md", ".markdown", ".txt":
		return ConvertTextToMarkdown(srcPath)
	case ".jpg", ".jpeg", ".png", ".bmp", ".tif", ".tiff":
		if !c.ocrCfg.Enabled || c.llmClient == nil {
			return nil, fmt.Errorf("localconvert: image conversion requires OCR to be enabled (系统设置 → 文件转换服务) and a doc_convert model to be configured")
		}
		return ConvertImageToMarkdown(ctx, c.llmClient, srcPath)
	default:
		return nil, fmt.Errorf("localconvert: unsupported format %s (only .xlsx, .docx, .pdf, .pptx and image files are implemented)", filepath.Ext(srcPath))
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
