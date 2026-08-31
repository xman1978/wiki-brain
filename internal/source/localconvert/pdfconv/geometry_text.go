package pdfconv

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ivanvanderbyl/docmill/v2/pkg/geom"
	"github.com/ivanvanderbyl/docmill/v2/pkg/page"
	docpdf "github.com/ivanvanderbyl/docmill/v2/pkg/pdf"
)

var (
	orderedListMarkerPrefixDigitRe = regexp.MustCompile(`^\d+[.、)）\]]`)
	orderedListMarkerPrefixParenRe = regexp.MustCompile(`^[（(]\s*\d+\s*[)）]`)
	embeddedOrderedMarkerDigitRe   = regexp.MustCompile(`\d+[.、)）\]]`)
	embeddedOrderedMarkerParenRe   = regexp.MustCompile(`[（(]\s*\d+\s*[)）]`)
	dateAtLineEndRe                = regexp.MustCompile(`(\d{4}年\d{1,2}月\d{1,2}日)\s*$`)
	orgSuffixAtEndRe               = regexp.MustCompile(`([\x{4E00}-\x{9FFF}]{4,}(?:分局|人民政府|委员会|支队|大队|局))\s*$`)
)

// isOrderedListMarkerPrefix ports ORDERED_LIST_MARKER_PREFIX (whole-line
// check, no capture) with the "(?!\d)" boundary enforced manually.
func isOrderedListMarkerPrefix(t string) bool {
	if loc := orderedListMarkerPrefixDigitRe.FindStringIndex(t); loc != nil && loc[0] == 0 {
		if numericBoundaryOK(t, loc[1], false) {
			return true
		}
	}
	if loc := orderedListMarkerPrefixParenRe.FindStringIndex(t); loc != nil && loc[0] == 0 {
		return true
	}
	return false
}

// groupFragmentsIntoLines ports pdf-port/01 §groupFragmentsIntoLines.
// Fragments must already be in BOTTOMLEFT origin.
func groupFragmentsIntoLines(fragments []page.TextCell, pageHeight float64, cfg Config) [][]page.TextCell {
	if len(fragments) == 0 {
		return nil
	}
	sorted := make([]page.TextCell, len(fragments))
	copy(sorted, fragments)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti := topDistanceFromPage(sorted[i].Box, pageHeight)
		tj := topDistanceFromPage(sorted[j].Box, pageHeight)
		if ti != tj {
			return ti < tj
		}
		return boxLLX(sorted[i].Box) < boxLLX(sorted[j].Box)
	})

	yTol := math.Max(1.0, cfg.YMergePt) * 1.6
	var groups []*lineGroup
	for _, f := range sorted {
		r := f.Box
		top := topDistanceFromPage(r, pageHeight)
		var best *lineGroup
		bestDy := math.Inf(1)
		for _, g := range groups {
			dy := math.Abs(top - g.topMean)
			if dy <= yTol && verticalOverlapRatio(r, g.yMin, g.yMax) >= 0.25 {
				if dy < bestDy {
					bestDy = dy
					best = g
				}
			}
		}
		if best == nil {
			best = &lineGroup{topMean: top, yMin: boxLLY(r), yMax: boxURY(r)}
			groups = append(groups, best)
		}
		best.items = append(best.items, f)
		best.update(top, boxLLY(r), boxURY(r))
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].topMean < groups[j].topMean })
	out := make([][]page.TextCell, len(groups))
	for i, g := range groups {
		out[i] = g.items
	}
	return out
}

// shouldBreakBeforeEmbeddedOrderedListMarker ports pdf-port/01's namesake.
func shouldBreakBeforeEmbeddedOrderedListMarker(assembledLineText, nextPart string) bool {
	if strings.TrimSpace(assembledLineText) == "" || strings.TrimSpace(nextPart) == "" {
		return false
	}
	if !IsListItem(strings.TrimSpace(assembledLineText)) {
		return false
	}
	return isOrderedListMarkerPrefix(strings.TrimSpace(nextPart))
}

// splitLineFragmentsByEmbeddedOrderedListMarker ports the namesake function.
func splitLineFragmentsByEmbeddedOrderedListMarker(lineFragments []page.TextCell) [][]page.TextCell {
	var out [][]page.TextCell
	var current []page.TextCell
	var assembled strings.Builder
	var prevBox *geom.Box
	var prevText string

	for _, f := range lineFragments {
		part := strings.TrimSpace(f.Text)
		if part == "" {
			continue
		}
		if len(current) > 0 && shouldBreakBeforeEmbeddedOrderedListMarker(assembled.String(), part) {
			out = append(out, current)
			current = nil
			assembled.Reset()
			prevBox = nil
			prevText = ""
		}
		box := f.Box
		if len(current) > 0 && shouldInsertSpaceByGeometry(prevText, part, prevBox, &box) {
			assembled.WriteByte(' ')
		}
		assembled.WriteString(part)
		current = append(current, f)
		prevText = part
		bc := box
		prevBox = &bc
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	if len(out) == 0 {
		return [][]page.TextCell{lineFragments}
	}
	return out
}

func detectHeadingPrefixStyleMismatch(rawText string, tokens []styleToken) bool {
	loc := headingPrefixOnlyRe.FindStringIndex(rawText)
	if loc == nil {
		return false
	}
	prefixEnd := len([]rune(rawText[:loc[1]]))
	rawRunes := []rune(rawText)
	if prefixEnd <= 0 || prefixEnd >= len(rawRunes) {
		return false
	}
	rest := strings.TrimSpace(string(rawRunes[prefixEnd:]))
	if rest == "" {
		return false
	}
	prefixSig := dominantStyle(tokens, 0, prefixEnd)
	restSig := dominantStyle(tokens, prefixEnd, len(rawRunes))
	if prefixSig == nil || restSig == nil {
		return false
	}
	diff := 0
	if normalizePdfFontFamilyForCompare(prefixSig.fontFamily) != normalizePdfFontFamilyForCompare(restSig.fontFamily) {
		diff++
	}
	prefixBold := prefixSig.fontWeight >= 600
	restBold := restSig.fontWeight >= 600
	if prefixBold != restBold {
		diff++
	}
	if math.Abs(prefixSig.fontSize-restSig.fontSize) >= 0.8 {
		diff++
	}
	return diff >= 1
}

func normalizePdfFontFamilyForCompare(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func dominantStyle(tokens []styleToken, start, end int) *styleSignature {
	weights := map[string]float64{}
	sigs := map[string]styleSignature{}
	for _, tok := range tokens {
		overlapStart := max(start, tok.start)
		overlapEnd := min(end, tok.end)
		if overlapEnd <= overlapStart {
			continue
		}
		key := normalizePdfFontFamilyForCompare(tok.fontFamily) + "|" +
			boolLabel(tok.fontWeight >= 700) + "|" + roundTo1(tok.fontSize)
		weights[key] += float64(overlapEnd - overlapStart)
		sigs[key] = styleSignature{fontFamily: tok.fontFamily, fontWeight: tok.fontWeight, fontSize: tok.fontSize}
	}
	best := ""
	bestW := -1.0
	for k, w := range weights {
		if w > bestW {
			bestW = w
			best = k
		}
	}
	if best == "" {
		return nil
	}
	sig := sigs[best]
	return &sig
}

func boolLabel(b bool) string {
	if b {
		return "b"
	}
	return "n"
}

func roundTo1(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', 1, 64)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildLineBlock ports pdf-port/01 §buildLineBlock.
func buildLineBlock(fragments []page.TextCell, pageNo int, pageHeight, pageWidth, bodyFontMode float64, index int, cfg Config) TextBlock {
	var text strings.Builder
	fontSizeSum := 0.0
	fontWeightMax := 400
	fontFamily := "unknown"
	italic := false
	mono := false
	left := math.Inf(1)
	right := math.Inf(-1)
	bottom := math.Inf(1)
	topY := math.Inf(-1)
	top := math.Inf(1)
	lineHeight := 0.0
	var tokens []styleToken
	textCursor := 0

	var prevBox *geom.Box
	var prevText string
	hadValidFragment := false

	for _, f := range fragments {
		box := f.Box
		if box == (geom.Box{}) && f.Text == "" {
			continue
		}
		part := f.Text
		if strings.TrimSpace(part) == "" {
			continue
		}
		if prevBox != nil && prevText == part && overlapRatio(box, *prevBox) >= 0.5 {
			continue
		}
		if text.Len() > 0 && shouldInsertSpaceByGeometry(prevText, part, prevBox, &box) {
			text.WriteByte(' ')
			textCursor++
		}
		text.WriteString(part)

		fs := f.FontSize
		partFontFamily := "unknown"
		if strings.TrimSpace(f.FontName) != "" {
			partFontFamily = f.FontName
		}
		partWeight := 400
		if f.FontFlags&page.FontFlagItalic != 0 {
			italic = true
		}
		if f.IsBold() {
			partWeight = 700
		}

		partLen := len([]rune(part))
		tokens = append(tokens, styleToken{start: textCursor, end: textCursor + partLen, fontFamily: partFontFamily, fontWeight: partWeight, fontSize: fs})
		textCursor += partLen

		fontSizeSum += fs
		if h := boxURY(box) - boxLLY(box); h > lineHeight {
			lineHeight = h
		}
		if v := boxLLX(box); v < left {
			left = v
		}
		if v := boxURX(box); v > right {
			right = v
		}
		if v := boxLLY(box); v < bottom {
			bottom = v
		}
		if v := boxURY(box); v > topY {
			topY = v
		}
		if v := topDistanceFromPage(box, pageHeight); v < top {
			top = v
		}
		if strings.TrimSpace(f.FontName) != "" {
			fontFamily = f.FontName
			lower := strings.ToLower(f.FontName)
			if strings.Contains(lower, "courier") || strings.Contains(lower, "consolas") || strings.Contains(lower, "mono") {
				mono = true
			}
		}
		if partWeight >= 700 && fontWeightMax < 700 {
			fontWeightMax = 700
		}
		prevText = part
		bc := box
		prevBox = &bc
		hadValidFragment = true
	}

	if math.IsInf(left, 1) {
		left = 0
	}
	if math.IsInf(top, 1) {
		top = 0
	}

	var bbox *geom.Box
	if right >= left && topY >= bottom {
		b := geom.Box{L: left, B: bottom, R: right, T: topY, Origin: geom.BottomLeft}
		bbox = &b
	}

	avgFont := bodyFontMode
	if len(fragments) > 0 {
		avgFont = fontSizeSum / float64(max(1, len(fragments)))
	}

	rawText := text.String()
	normalized := normalizeText(rawText, cfg)
	mismatch := false
	if hadValidFragment {
		mismatch = detectHeadingPrefixStyleMismatch(rawText, tokens)
	}
	if lineHeight <= 0 {
		lineHeight = avgFont * 1.2
	}

	return TextBlock{
		baseElement: baseElement{
			ID:          blockID("TextBlock", pageNo, index),
			PageNo:      pageNo,
			Bbox:        bbox,
			TopDistance: top,
			Left:        left,
		},
		Text:                       normalized,
		FontSizeMean:               avgFont,
		FontFamily:                 fontFamily,
		FontWeight:                 fontWeightMax,
		Italic:                     italic,
		MonoFont:                   mono,
		LineHeight:                 lineHeight,
		IndentLeft:                 left,
		TableID:                    -1,
		BodyFontMode:               bodyFontMode,
		HeadingLastLineTop:         math.NaN(),
		HeadingTrailingLeft:        math.NaN(),
		HeadingTrailingText:        "",
		PageWidth:                  pageWidth,
		HeadingPrefixStyleMismatch: mismatch,
	}
}

func blockID(prefix string, pageNo, index int) string {
	return prefix + "_" + strconv.Itoa(pageNo) + "_" + strconv.Itoa(index)
}

// splitTailOrgDateIfNeeded ports pdf-port/01's namesake.
func splitTailOrgDateIfNeeded(block TextBlock) []TextBlock {
	if !IsListItem(block.Text) {
		return []TextBlock{block}
	}
	loc := dateAtLineEndRe.FindStringSubmatchIndex(block.Text)
	if loc == nil {
		return []TextBlock{block}
	}
	date := block.Text[loc[2]:loc[3]]
	dateStart := loc[2]
	beforeDate := strings.TrimSpace(block.Text[:dateStart])
	if beforeDate == "" || date == "" {
		return []TextBlock{block}
	}
	orgLoc := orgSuffixAtEndRe.FindStringSubmatchIndex(beforeDate)
	if orgLoc == nil {
		return []TextBlock{block}
	}
	org := beforeDate[orgLoc[2]:orgLoc[3]]
	orgStart := orgLoc[2]
	listBody := strings.TrimSpace(beforeDate[:orgStart])
	if listBody == "" || org == "" {
		return []TextBlock{block}
	}
	return []TextBlock{block.WithText(listBody), block.WithText(org), block.WithText(date)}
}

// splitTextBlockByEmbeddedOrderedMarkers ports pdf-port/01's namesake.
func splitTextBlockByEmbeddedOrderedMarkers(block TextBlock) []TextBlock {
	starts := embeddedOrderedMarkerStarts(block.Text)
	if len(starts) < 2 {
		return splitTailOrgDateIfNeeded(block)
	}
	var out []TextBlock
	runes := []rune(block.Text)
	if starts[0] > 0 {
		head := strings.TrimSpace(string(runes[:starts[0]]))
		if head != "" {
			out = append(out, splitTailOrgDateIfNeeded(block.WithText(head))...)
		}
	}
	for i, s := range starts {
		e := len(runes)
		if i+1 < len(starts) {
			e = starts[i+1]
		}
		chunk := strings.TrimSpace(string(runes[s:e]))
		if chunk == "" {
			continue
		}
		out = append(out, splitTailOrgDateIfNeeded(block.WithText(chunk))...)
	}
	if len(out) == 0 {
		return []TextBlock{block}
	}
	return out
}

// embeddedOrderedMarkerStarts returns rune-offset match starts for
// EMBEDDED_ORDERED_LIST_MARKER = (?<!\d)(?:\d+[.、)）\]](?!\d)|[（(]\s*\d+\s*[)）]),
// enforced manually since RE2 has no lookaround.
func embeddedOrderedMarkerStarts(text string) []int {
	runes := []rune(text)
	byteToRune := make(map[int]int, len(runes)+1)
	pos := 0
	for i, r := range text {
		byteToRune[i] = pos
		_ = r
		pos++
	}
	byteToRune[len(text)] = pos

	var starts []int
	seen := map[int]bool{}
	for _, loc := range embeddedOrderedMarkerDigitRe.FindAllStringIndex(text, -1) {
		if !numericBoundaryOK(text, loc[1], false) {
			continue
		}
		if loc[0] > 0 {
			prev := firstRuneBefore(text, loc[0])
			if prev >= '0' && prev <= '9' {
				continue
			}
			// A digit run immediately preceded by an ASCII Latin letter
			// (e.g. the "3" in "（A3）协作类别判断", a cross-reference code,
			// not a list marker) is not a standalone ordered-list marker —
			// it's part of an alphanumeric code. The ported Java regex has
			// no such exclusion (its negative lookbehind only excludes a
			// preceding digit), but that's a genuine defect surfaced by
			// real-document testing (docs/impl/v1/local-file-convert.md's
			// PDF section): "3）" inside "（A3）" was being treated as a
			// second marker alongside the real "4、" prefix, splitting one
			// list item's text into unrelated fragments. This must stay
			// ASCII-only (not unicode.IsLetter, which is also true for
			// every CJK ideograph): this function's whole purpose is
			// re-splitting lines like "总则2.分则3.附则" where each bare
			// digit marker directly follows the previous item's Chinese
			// text — unicode.IsLetter would wrongly swallow that back into
			// a single unsplit line, breaking the very case this exists
			// for. ASCII letters are the actual shape of the alphanumeric
			// reference codes this excludes ("A3", "B12", "C4", ...), not
			// just this document's "A3/A4/A8/A9".
			if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') {
				continue
			}
		}
		// The two alternatives of the ported EMBEDDED_ORDERED_LIST_MARKER
		// regex — bare "N." forms and parenthesized "（N）" forms — are a
		// single alternation in the original (non-overlapping find, tries
		// the leftmost alternative first), but Go's RE2 has no lookaround
		// so they're scanned as two independent passes here. Without this
		// check, a marker like "（1）" gets matched TWICE: once whole by
		// embeddedOrderedMarkerParenRe starting at "（", and once again by
		// this digit-only regex starting one rune later at "1）" — turning
		// one marker into two split points and shearing the "（" off into
		// its own orphan chunk. Skip a digit-only match that's just the
		// tail of an already-open paren marker.
		if precededByOpenParen(text, loc[0]) {
			continue
		}
		r := byteToRune[loc[0]]
		if !seen[r] {
			seen[r] = true
			starts = append(starts, r)
		}
	}
	for _, loc := range embeddedOrderedMarkerParenRe.FindAllStringIndex(text, -1) {
		r := byteToRune[loc[0]]
		if !seen[r] {
			seen[r] = true
			starts = append(starts, r)
		}
	}
	sort.Ints(starts)
	return starts
}

// precededByOpenParen reports whether the nearest non-whitespace rune before
// byteIdx is an opening paren ("（"/"("), i.e. whether the digit run
// starting at byteIdx is really the tail of a "（N）"/"(N)" marker (matching
// embeddedOrderedMarkerParenRe's own "[（(]\s*\d+" shape) rather than a
// standalone bare "N." marker.
func precededByOpenParen(s string, byteIdx int) bool {
	sub := s[:byteIdx]
	runes := []rune(sub)
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if unicode.IsSpace(r) {
			continue
		}
		return r == '（' || r == '('
	}
	return false
}

func firstRuneBefore(s string, byteIdx int) rune {
	if byteIdx <= 0 {
		return 0
	}
	sub := s[:byteIdx]
	runes := []rune(sub)
	if len(runes) == 0 {
		return 0
	}
	return runes[len(runes)-1]
}

// buildRawTextBlocks ports pdf-port/01 §buildRawTextBlocks.
func buildRawTextBlocks(fragments []page.TextCell, pageNo int, pageHeight, pageWidth float64, tables []TableBlock, bodyFontMode float64, cfg Config, filter *headerFooterFilter) []TextBlock {
	var filtered []page.TextCell
	for _, f := range fragments {
		if f.Box == (geom.Box{}) {
			continue
		}
		if isInsideAnyTable(f.Box, tables, cfg) {
			continue
		}
		filtered = append(filtered, f)
	}
	lines := groupFragmentsIntoLines(filtered, pageHeight, cfg)

	var out []TextBlock
	blockIndex := 0
	for _, line := range lines {
		sorted := make([]page.TextCell, len(line))
		copy(sorted, line)
		sort.SliceStable(sorted, func(i, j int) bool { return boxLLX(sorted[i].Box) < boxLLX(sorted[j].Box) })

		subLines := splitLineFragmentsByEmbeddedOrderedListMarker(sorted)
		for _, sub := range subLines {
			block := buildLineBlock(sub, pageNo, pageHeight, pageWidth, bodyFontMode, blockIndex, cfg)
			blockIndex++
			if shouldDropAsHeaderFooterLine(sub, block, pageNo, pageHeight, cfg, filter) {
				continue
			}
			out = append(out, splitTextBlockByEmbeddedOrderedMarkers(block)...)
		}
	}
	return out
}

func isInsideAnyTable(rect geom.Box, tables []TableBlock, cfg Config) bool {
	for _, t := range tables {
		if t.Bbox == nil {
			continue
		}
		if overlapRatio(rect, *t.Bbox) >= cfg.TableOverlapRatio {
			return true
		}
	}
	return false
}

// estimateBodyFontMode ports pdf-port/01 §estimateBodyFontMode.
func estimateBodyFontMode(ctx context.Context, pg docpdf.Page, pageNo int, pageHeight, pageWidth float64, cfg Config, filter *headerFooterFilter) (float64, error) {
	cells, err := pg.TextCells(ctx)
	if err != nil {
		return 12.0, err
	}
	cells = toBottomLeftCells(cells, pageHeight)
	var valid []page.TextCell
	for _, c := range cells {
		if c.Box != (geom.Box{}) {
			valid = append(valid, c)
		}
	}
	lines := groupFragmentsIntoLines(valid, pageHeight, cfg)

	hist := map[int]int{}
	bestBucket := 24
	bestCount := 0
	for _, line := range lines {
		tmp := buildLineBlock(line, pageNo, pageHeight, pageWidth, 12.0, 0, cfg)
		if shouldDropAsHeaderFooterLine(line, tmp, pageNo, pageHeight, cfg, filter) {
			continue
		}
		for _, f := range line {
			bucket := int(math.Round(f.FontSize * 2))
			hist[bucket]++
		}
	}
	buckets := make([]int, 0, len(hist))
	for b := range hist {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)
	for _, b := range buckets {
		if hist[b] > bestCount {
			bestCount = hist[b]
			bestBucket = b
		}
	}
	return float64(bestBucket) / 2.0, nil
}
