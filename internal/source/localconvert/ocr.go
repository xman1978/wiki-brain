package localconvert

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// docConvertPromptFile is the shared OCR prompt for both the scanned-PDF and
// standalone-image conversion paths (docs/impl/v1/local-file-convert.md
// §10): images are sent in page order, and the model returns one merged
// Markdown document wrapped in a ```markdown fence.
const docConvertPromptFile = "doc_convert_image_to_markdown.md"

// docConvertPurpose is the LLM purpose bound on the model configuration page
// ("文档转换") to a Provider capable of image input.
const docConvertPurpose = "doc_convert"

// runDocConvertOCR sends images (already in page order) to the doc_convert
// model in a single call and returns the Markdown it produces, with no
// further restructuring — the model is required by the prompt to return the
// final Markdown as-is.
func runDocConvertOCR(ctx context.Context, llmClient llm.LLMClient, images []llm.ImageInput) ([]byte, error) {
	if llmClient == nil {
		return nil, fmt.Errorf("localconvert: ocr: no LLM client configured")
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("localconvert: ocr: no images to convert")
	}
	vars := map[string]string{"page_count": strconv.Itoa(len(images))}
	raw, err := llmClient.CompleteImage(ctx, docConvertPromptFile, vars, images, docConvertPurpose)
	if err != nil {
		return nil, fmt.Errorf("localconvert: ocr: model call failed: %w", err)
	}
	md := stripMarkdownFence(raw)
	if strings.TrimSpace(md) == "" {
		return nil, fmt.Errorf("localconvert: ocr: model returned empty markdown")
	}
	return []byte(md), nil
}

// stripMarkdownFence extracts the content of the outermost ```markdown ...
// ``` fence the doc_convert_image_to_markdown.md prompt requires the model
// to wrap its output in. If no such fence is present (a model that ignored
// the instruction), the raw output is returned unchanged rather than
// dropped, since the fence is a parsing aid, not a content contract.
func stripMarkdownFence(raw string) string {
	s := strings.TrimSpace(raw)
	start := strings.Index(s, "```markdown")
	fenceLen := len("```markdown")
	if start == -1 {
		start = strings.Index(s, "```")
		fenceLen = len("```")
		if start == -1 {
			return s
		}
	}
	after := s[start+fenceLen:]
	after = strings.TrimPrefix(after, "\n")
	end := strings.LastIndex(after, "```")
	if end == -1 {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(after[:end])
}

// mimeForImageExt maps a file extension to the MIME type sent in the
// data: URI for CompleteImage. Only formats commonly accepted by
// OpenAI-compatible multimodal chat endpoints are covered.
func mimeForImageExt(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".png":
		return "image/png", true
	case ".bmp":
		return "image/bmp", true
	case ".tif", ".tiff":
		return "image/tiff", true
	default:
		return "", false
	}
}

// mimeForPDFImageFileType maps the FileType pdfcpu reports for an embedded
// PDF image resource (writeImage.go: renderImage/RenderImage) to a MIME
// type. "jpx" (JPEG2000) and "jbig2" are returned undecoded raw codestreams
// by pdfcpu, which multimodal chat endpoints do not accept as image input —
// ok is false for those so the caller can surface an explicit error instead
// of sending bytes the model cannot decode.
func mimeForPDFImageFileType(fileType string) (string, bool) {
	switch strings.ToLower(fileType) {
	case "jpg", "jpeg":
		return "image/jpeg", true
	case "png":
		return "image/png", true
	case "tif", "tiff":
		return "image/tiff", true
	default:
		return "", false
	}
}
