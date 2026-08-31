package pdfconv

import (
	"context"
	"errors"
	"sort"

	docpdf "github.com/ivanvanderbyl/docmill/v2/pkg/pdf"
)

// ErrNoExtractableText is returned when a PDF has zero extractable text
// cells across every page — the docmill pure-Go backend has no OCR path, so
// a scanned/image-only PDF (docs/impl/v1/local-file-convert.md §6.5 "不支持
// 扫描件/图片型 PDF") must surface an explicit error here, not a silent
// empty conversion.
var ErrNoExtractableText = errors.New("localconvert/pdfconv: no extractable text found (scanned/image-only PDF is not supported — OCR is out of scope)")

// extractTextBlocks ports pdf-port/01 convertDocument step 2d: raw
// extraction followed by same-page line merging.
func extractTextBlocks(ctx context.Context, pg docpdf.Page, pageNo int, pageHeight, pageWidth float64, tables []TableBlock, bodyFontMode float64, cfg Config, filter *headerFooterFilter) ([]TextBlock, error) {
	cells, err := pg.TextCells(ctx)
	if err != nil {
		return nil, err
	}
	cells = toBottomLeftCells(cells, pageHeight)
	raw := buildRawTextBlocks(cells, pageNo, pageHeight, pageWidth, tables, bodyFontMode, cfg, filter)
	merged := mergeLines(raw, bodyFontMode, cfg)
	return merged, nil
}

// ConvertDocument ports pdf-port/01 §convertDocument (extraction half) plus
// the Part 2 rendering reduction documented in render.go. It returns the
// final Markdown text after also passing through the shared
// RunMarkdownPipeline cleanup stage (pipeline.go).
func ConvertDocument(ctx context.Context, doc docpdf.Document, cfg Config) (string, error) {
	pageCount, err := doc.PageCount(ctx)
	if err != nil {
		return "", err
	}
	filter, err := detectHeaderFooterFilter(ctx, doc, pageCount, cfg)
	if err != nil {
		return "", err
	}

	var elements []GeometricElement
	totalCells := 0
	for i := 0; i < pageCount; i++ {
		pageNo := i + 1
		pg, err := doc.Page(ctx, i)
		if err != nil {
			return "", err
		}
		size, err := pg.Size(ctx)
		if err != nil {
			return "", err
		}
		pageHeight := size.Height
		pageWidth := size.Width

		if cells, err := pg.TextCells(ctx); err == nil {
			totalCells += len(cells)
		}

		tables, err := extractTableBlocks(ctx, pg, pageNo, pageHeight, pageWidth, cfg)
		if err != nil {
			return "", err
		}
		bodyFontMode, err := estimateBodyFontMode(ctx, pg, pageNo, pageHeight, pageWidth, cfg, filter)
		if err != nil {
			return "", err
		}
		textBlocks, err := extractTextBlocks(ctx, pg, pageNo, pageHeight, pageWidth, tables, bodyFontMode, cfg, filter)
		if err != nil {
			return "", err
		}

		if cfg.RemovePageNumbers {
			var kept []TextBlock
			for _, tb := range textBlocks {
				if tb.Bbox != nil && isPageNumberBlock(tb.Text, tb.TopDistance, pageHeight, cfg) {
					continue
				}
				kept = append(kept, tb)
			}
			textBlocks = kept
		}

		for _, t := range tables {
			elements = append(elements, t)
		}
		for _, t := range textBlocks {
			elements = append(elements, t)
		}
	}

	if totalCells == 0 {
		return "", ErrNoExtractableText
	}

	sort.SliceStable(elements, func(i, j int) bool {
		a, b := elements[i], elements[j]
		if a.ElemPageNo() != b.ElemPageNo() {
			return a.ElemPageNo() < b.ElemPageNo()
		}
		if a.ElemTopDistance() != b.ElemTopDistance() {
			return a.ElemTopDistance() < b.ElemTopDistance()
		}
		return a.ElemLeft() < b.ElemLeft()
	})

	elements = mergeCrossPageTables(elements)
	elements = demoteDecorativeSingleCellTables(elements)
	elements = mergeCrossPageParagraphBlocks(elements, cfg)

	raw, protectedLeftTexts := renderMarkdown(elements, cfg)
	return RunMarkdownPipelineForPDF(raw, protectedLeftTexts), nil
}

// ConvertPDFToMarkdown is the localconvert.pdf.go entry point described in
// docs/impl/v1/local-file-convert.md §6.3.
func ConvertPDFToMarkdown(ctx context.Context, data []byte, backend docpdf.Backend) ([]byte, error) {
	doc, err := backend.OpenBytes(ctx, data)
	if err != nil {
		return nil, err
	}
	defer doc.Close()

	cfg := DefaultConfig()
	md, err := ConvertDocument(ctx, doc, cfg)
	if err != nil {
		return nil, err
	}
	return []byte(md), nil
}
