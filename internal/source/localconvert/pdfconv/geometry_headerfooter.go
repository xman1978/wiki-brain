package pdfconv

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ivanvanderbyl/docmill/v2/pkg/geom"
	"github.com/ivanvanderbyl/docmill/v2/pkg/page"
	docpdf "github.com/ivanvanderbyl/docmill/v2/pkg/pdf"
)

var runOfDigitsRe = regexp.MustCompile(`\d+`)

var titleChapterRe = regexp.MustCompile(`^第\s*(?:[一二三四五六七八九十百千万零廿卅]+|\d+)\s*(章|节|纲|目|条).*`)

// shouldPreserveInHeaderFooterBand is ChapterTocLineRemover's namesake
// method (Part 2 territory; pdf-port/01 only records the call point). It
// protects lines that look like real chapter/section headings from being
// dropped just because they happen to fall in the header/footer geometric
// band — approximated here with the same structural-heading patterns
// already ported for the DOCX heading stage (chapter_toc.go).
func shouldPreserveInHeaderFooterBand(normalized string) bool {
	if normalized == "" {
		return false
	}
	return titleChapterRe.MatchString(normalized) ||
		IsStructuralChapterHeading(normalized) ||
		IsStructuralCnSectionHeading(normalized)
}

// headerFooterSignature ports the Java method of the same name
// (pdf-port/01 §detectHeaderFooterFilter).
func headerFooterSignature(text string, cfg Config) string {
	sig := strings.ToLower(normalizeText(text, cfg))
	sig = runOfDigitsRe.ReplaceAllString(sig, "#")
	var b strings.Builder
	for _, r := range sig {
		if unicode.IsPunct(r) {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	sig = geomMultiSpace.ReplaceAllString(b.String(), " ")
	sig = strings.TrimSpace(sig)
	if len([]rune(sig)) < 2 {
		return ""
	}
	return sig
}

func isInHeaderFooterBandDist(topDistance, pageHeight float64, cfg Config) bool {
	return topDistance < pageHeight*cfg.HeaderTopRatio || topDistance > pageHeight*cfg.FooterBottomRatio
}

func isInHeaderFooterBandRect(rect geom.Box, pageHeight float64, cfg Config) bool {
	return isInHeaderFooterBandDist(topDistanceFromPage(rect, pageHeight), pageHeight, cfg)
}

// isHeaderOrFooter ports pdf-port/01 §isHeaderOrFooter.
func isHeaderOrFooter(rect geom.Box, pageNo int, pageHeight float64, cfg Config, filter *headerFooterFilter, lineText string) bool {
	if filter != nil && isInHeaderFooterBandRect(rect, pageHeight, cfg) {
		if _, ok := filter.repeatedSignatures[headerFooterSignature(lineText, cfg)]; ok {
			return true
		}
	}
	if shouldPreserveInHeaderFooterBand(normalizeText(lineText, cfg)) {
		return false
	}
	if filter != nil {
		zones := filter.repeatedZonesByPage[pageNo]
		if len(zones) > 0 {
			for _, z := range zones {
				if overlapRatio(rect, z) >= 0.30 {
					return true
				}
			}
			return false
		}
		if filter.pageCount <= 1 {
			return isInHeaderFooterBandRect(rect, pageHeight, cfg)
		}
		return false
	}
	return isInHeaderFooterBandRect(rect, pageHeight, cfg)
}

func unionFragmentBBox(fragments []page.TextCell) *geom.Box {
	if len(fragments) == 0 {
		return nil
	}
	first := true
	var out geom.Box
	for _, f := range fragments {
		b := f.Box
		if first {
			out = geom.Box{L: boxLLX(b), B: boxLLY(b), R: boxURX(b), T: boxURY(b), Origin: geom.BottomLeft}
			first = false
			continue
		}
		out = unionRect(out, b)
	}
	if first {
		return nil
	}
	return &out
}

// shouldDropAsHeaderFooterLine ports pdf-port/01 §shouldDropAsHeaderFooterLine.
// block is the already-assembled TextBlock (its TopDistance backs the
// degenerate fallback when the raw fragments yield no valid union bbox).
func shouldDropAsHeaderFooterLine(fragments []page.TextCell, block TextBlock, pageNo int, pageHeight float64, cfg Config, filter *headerFooterFilter) bool {
	if strings.TrimSpace(block.Text) == "" {
		return false
	}
	normalized := normalizeText(block.Text, cfg)
	if normalized == "" {
		return false
	}
	bbox := unionFragmentBBox(fragments)
	if bbox == nil {
		if !shouldPreserveInHeaderFooterBand(normalized) {
			return isInHeaderFooterBandDist(block.TopDistance, pageHeight, cfg)
		}
		return false
	}
	return isHeaderOrFooter(*bbox, pageNo, pageHeight, cfg, filter, block.Text)
}

func isPageNumberBlock(blockRawText string, topDistance, pageHeight float64, cfg Config) bool {
	if !isPageNumberBlockText(blockRawText) {
		return false
	}
	return topDistance < pageHeight*cfg.HeaderPageNumberRatio || topDistance > pageHeight*cfg.FooterPageNumberRatio
}

// detectHeaderFooterFilter ports pdf-port/01 §detectHeaderFooterFilter.
func detectHeaderFooterFilter(ctx context.Context, doc docpdf.Document, pageCount int, cfg Config) (*headerFooterFilter, error) {
	filter := &headerFooterFilter{
		pageCount:           pageCount,
		repeatedZonesByPage: map[int][]geom.Box{},
		repeatedSignatures:  map[string]struct{}{},
	}
	if pageCount <= 0 {
		return filter, nil
	}

	var candidates []headerFooterLine
	signaturePages := map[string]map[int]struct{}{}

	for i := 0; i < pageCount; i++ {
		pg, err := doc.Page(ctx, i)
		if err != nil {
			return nil, err
		}
		size, err := pg.Size(ctx)
		if err != nil {
			return nil, err
		}
		cells, err := pg.TextCells(ctx)
		if err != nil {
			return nil, err
		}
		cells = toBottomLeftCells(cells, size.Height)
		lines := groupFragmentsIntoLines(cells, size.Height, cfg)
		pageNo := i + 1
		for _, line := range lines {
			cand := buildHeaderFooterLineCandidate(line, pageNo, size.Height, cfg)
			if cand == nil {
				continue
			}
			candidates = append(candidates, *cand)
			if signaturePages[cand.signature] == nil {
				signaturePages[cand.signature] = map[int]struct{}{}
			}
			signaturePages[cand.signature][pageNo] = struct{}{}
		}
	}

	minRepeatedPages := int(math.Max(2, math.Ceil(float64(pageCount)*0.30)))
	for sig, pages := range signaturePages {
		if len(pages) >= minRepeatedPages {
			filter.repeatedSignatures[sig] = struct{}{}
		}
	}
	for _, cand := range candidates {
		if _, ok := filter.repeatedSignatures[cand.signature]; ok {
			filter.repeatedZonesByPage[cand.pageNo] = append(filter.repeatedZonesByPage[cand.pageNo], cand.bbox)
		}
	}
	return filter, nil
}

func buildHeaderFooterLineCandidate(fragments []page.TextCell, pageNo int, pageHeight float64, cfg Config) *headerFooterLine {
	if len(fragments) == 0 {
		return nil
	}
	sorted := make([]page.TextCell, len(fragments))
	copy(sorted, fragments)
	sort.SliceStable(sorted, func(i, j int) bool { return boxLLX(sorted[i].Box) < boxLLX(sorted[j].Box) })

	var textBuilder strings.Builder
	var prevText string
	var prevBox *geom.Box
	first := true
	var union geom.Box
	for _, f := range sorted {
		t := f.Text
		if strings.TrimSpace(t) == "" {
			continue
		}
		box := f.Box
		if !first && shouldInsertSpaceByGeometry(prevText, t, prevBox, &box) {
			textBuilder.WriteByte(' ')
		}
		textBuilder.WriteString(t)
		if first {
			union = geom.Box{L: boxLLX(box), B: boxLLY(box), R: boxURX(box), T: boxURY(box), Origin: geom.BottomLeft}
			first = false
		} else {
			union = unionRect(union, box)
		}
		prevText = t
		bc := box
		prevBox = &bc
	}
	text := textBuilder.String()
	if text == "" || first {
		return nil
	}
	if !isInHeaderFooterBandRect(union, pageHeight, cfg) {
		return nil
	}
	sig := headerFooterSignature(text, cfg)
	if sig == "" {
		return nil
	}
	return &headerFooterLine{pageNo: pageNo, bbox: union, signature: sig}
}

// glyphNormalizedHeightRatio / glyphSmallInkRatioThreshold: docmill returns
// TIGHT ink bounding boxes per glyph, whereas the ported algorithms (and the
// Aspose rectangles they were written against) assume every fragment in a
// line reports a roughly uniform, font-size-proportional box. A CJK
// full-width character's ink box is normally ~0.90x its font size, but
// small marks are far shorter — and *where* the surviving ink sits varies
// by glyph shape, not just its height, so a single anchor edge can't fix
// every case (confirmed against real extracted cells from two different
// documents): fullwidth CJK punctuation (，。、－ dot-leaders etc.) is drawn
// hanging low in the lower part of its em-square by typographic
// convention, so its BOTTOM edge lines up with normal characters' bottoms;
// but a stroke-only Han ideograph like "一" is drawn centered in its
// em-square (ideographs, unlike punctuation, are not given that
// low-hanging treatment), so its ink CENTER lines up with normal
// characters' centers while its bottom sits well above theirs — anchoring
// a "一" on its bottom overshoots the reconstructed top by 6pt+.
// A small, explicit set of low-hanging punctuation (drawn near the
// baseline by typographic convention, both the CJK fullwidth forms and
// their ASCII/halfwidth equivalents used as dot-leaders) is bottom-anchored;
// everything else short (Han ideographs like "一", quotation marks, which
// sit high rather than low, etc.) is center-anchored as the safer default
// — bottom-anchoring a high-sitting glyph like a closing quote overshoots
// just as badly as top-anchoring a low-hanging one did originally. Only
// glyphs whose ink is suspiciously short relative to font size are touched
// at all; normal characters' own ink extent is trusted as-is.
const (
	glyphNormalizedHeightRatio  = 0.9
	glyphSmallInkRatioThreshold = 0.5
)

var lowHangingPunctuation = map[rune]bool{
	'，': true, '。': true, '、': true, '；': true, '：': true, '！': true, '？': true,
	',': true, '.': true, ';': true, ':': true, '!': true, '?': true,
	'-': true, '—': true, '－': true, '·': true,
}

func toBottomLeftCells(cells []page.TextCell, pageHeight float64) []page.TextCell {
	out := make([]page.TextCell, len(cells))
	for i, c := range cells {
		c.Box = toBottomLeft(c.Box, pageHeight)
		if c.FontSize > 0 {
			height := c.Box.T - c.Box.B
			if height < c.FontSize*glyphSmallInkRatioThreshold {
				half := c.FontSize * glyphNormalizedHeightRatio / 2
				if lowHangingPunctuation[firstNonSpaceChar(c.Text)] {
					c.Box.T = c.Box.B + half*2
				} else {
					center := (c.Box.T + c.Box.B) / 2
					c.Box.T = center + half
					c.Box.B = center - half
				}
			}
		}
		out[i] = c
	}
	return out
}
