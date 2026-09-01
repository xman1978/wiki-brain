package localconvert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// ConvertImageToMarkdown implements docs/impl/v1/local-file-convert.md §10:
// a standalone image file is sent to the doc_convert model as a single-image
// OCR call, with no page batching involved.
func ConvertImageToMarkdown(ctx context.Context, llmClient llm.LLMClient, srcPath string) ([]byte, error) {
	mime, ok := mimeForImageExt(filepath.Ext(srcPath))
	if !ok {
		return nil, fmt.Errorf("localconvert: unsupported image format %s", filepath.Ext(srcPath))
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: read image: %w", err)
	}
	return runDocConvertOCR(ctx, llmClient, []llm.ImageInput{{Data: data, MimeType: mime}})
}
