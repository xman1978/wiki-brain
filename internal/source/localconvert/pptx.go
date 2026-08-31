package localconvert

import (
	"fmt"

	"github.com/jxman78/wiki-brain/internal/source/localconvert/pptx"
)

// ConvertPptxToMarkdown implements docs/impl/v1/local-file-convert.md §7:
// FileView has no PPT-to-Markdown pipeline to port, so this adopts the
// inlined internal/source/localconvert/pptx package as-is.
func ConvertPptxToMarkdown(srcPath string) ([]byte, error) {
	md, err := pptx.Convert(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: convert pptx: %w", err)
	}
	return []byte(md), nil
}
