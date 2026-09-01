package localconvert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"

	"github.com/ivanvanderbyl/docmill/v2/pkg/parser"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpumodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/image/tiff"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/source/localconvert/pdfconv"
)

// ConvertPDFToMarkdown implements docs/impl/v1/local-file-convert.md §6.3:
// parse the PDF with docmill's pure-Go backend, then run the ported
// PdfToMarkdown pipeline (internal/source/localconvert/pdfconv). A scanned
// PDF (pdfconv.ErrNoExtractableText) falls back to OCR via
// convertScannedPDFViaOCR (§10) when ocrCfg.Enabled and llmClient is
// configured; otherwise the original error is returned unchanged.
func ConvertPDFToMarkdown(ctx context.Context, srcPath string, llmClient llm.LLMClient, ocrCfg OCRSettings) ([]byte, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("localconvert: read pdf: %w", err)
	}
	backend := parser.NewBackend()
	defer backend.Close()

	md, err := pdfconv.ConvertPDFToMarkdown(ctx, data, backend)
	if err == nil {
		return md, nil
	}
	if !errors.Is(err, pdfconv.ErrNoExtractableText) {
		return nil, fmt.Errorf("localconvert: convert pdf: %w", err)
	}
	if !ocrCfg.Enabled || llmClient == nil {
		return nil, fmt.Errorf("localconvert: convert pdf: %w", err)
	}
	return convertScannedPDFViaOCR(ctx, data, llmClient, ocrCfg)
}

// convertScannedPDFViaOCR extracts each page's embedded scan image with
// pdfcpu (pure Go, no PDF-page rendering — docs/impl/v1/local-file-convert.md
// §10) and sends the whole page set to the doc_convert model in a single
// call so it can merge content spanning page boundaries, rather than
// converting page-by-page and concatenating independently.
func convertScannedPDFViaOCR(ctx context.Context, data []byte, llmClient llm.LLMClient, ocrCfg OCRSettings) ([]byte, error) {
	maxPages := ocrCfg.MaxPages
	if maxPages <= 0 {
		maxPages = 50
	}

	pageCount, err := pdfcpuapi.PageCount(bytes.NewReader(data), nil)
	if err != nil {
		return nil, fmt.Errorf("localconvert: ocr: count pdf pages: %w", err)
	}
	if pageCount > maxPages {
		return nil, fmt.Errorf("localconvert: ocr: scanned pdf has %d pages, exceeds max_pages=%d (系统设置 → 文件转换服务)", pageCount, maxPages)
	}

	// selectedPages=nil selects every page in ascending order (pdfcpu
	// PagesForPageSelection with ensureAllforNone=true), so the slice index
	// below is the 0-based page number.
	pageImages, err := pdfcpuapi.ExtractImagesRaw(bytes.NewReader(data), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("localconvert: ocr: extract embedded page images: %w", err)
	}

	images := make([]llm.ImageInput, 0, len(pageImages))
	for i, pageMap := range pageImages {
		pageNr := i + 1
		fileType, imgBytes, err := pickPageImage(pageMap)
		if err != nil {
			return nil, fmt.Errorf("localconvert: ocr: page %d: %w", pageNr, err)
		}
		mime, ok := mimeForPDFImageFileType(fileType)
		if !ok {
			return nil, fmt.Errorf("localconvert: ocr: page %d embedded image format %q is not supported for OCR (only jpg/png/tiff)", pageNr, fileType)
		}
		images = append(images, llm.ImageInput{Data: imgBytes, MimeType: mime})
	}

	return runDocConvertOCR(ctx, llmClient, images)
}

// pickPageImage picks the embedded image most likely to be the full-page
// scan when a page has more than one (e.g. a scanned page plus a small
// header logo/stamp image): the one with the largest pixel area.
//
// pdfcpu's non-stub extraction path (used by ExtractImagesRaw) does not
// populate model.Image.Width/Height — those are only set by the separate
// metadata-only stub path — so the pixel area has to be measured here by
// decoding each candidate's image header ourselves.
func pickPageImage(pageMap map[int]pdfcpumodel.Image) (fileType string, data []byte, err error) {
	if len(pageMap) == 0 {
		return "", nil, fmt.Errorf("no embedded image")
	}
	bestArea := -1
	for _, img := range pageMap {
		b, err := io.ReadAll(img)
		if err != nil {
			return "", nil, fmt.Errorf("read embedded image: %w", err)
		}
		area := imagePixelArea(img.FileType, b)
		if area > bestArea {
			bestArea = area
			fileType, data = img.FileType, b
		}
	}
	return fileType, data, nil
}

// imagePixelArea decodes just the header of an embedded image to get its
// pixel dimensions. Unrecognized/undecodable formats return 0 so they lose
// the comparison rather than aborting the whole page-selection heuristic —
// mimeForPDFImageFileType still rejects unsupported formats afterward if
// the chosen image turns out to be one of them.
func imagePixelArea(fileType string, data []byte) int {
	var cfg image.Config
	var err error
	switch strings.ToLower(fileType) {
	case "jpg", "jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case "png":
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case "tif", "tiff":
		cfg, err = tiff.DecodeConfig(bytes.NewReader(data))
	default:
		return 0
	}
	if err != nil {
		return 0
	}
	return cfg.Width * cfg.Height
}
