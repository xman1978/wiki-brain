package pdfconv

import (
	"math"
	"regexp"
	"strings"
)

var continuationPunctuation = "，。；：、！？,.!?;:）)]】》"
var semanticBreakSuffixes = []string{"如下", "如下：", "包括", "如下所示"}

func endsWithSentenceTerminator(text string) bool {
	last := lastNonSpaceChar(text)
	return strings.ContainsRune("。.!！?？:：;；）)", last)
}

func endsWithSemanticBreak(text string) bool {
	t := strings.TrimSpace(text)
	for _, suf := range semanticBreakSuffixes {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

func startsWithContinuationPunctuation(text string) bool {
	first := firstNonSpaceChar(text)
	return first != 0 && strings.ContainsRune(continuationPunctuation, first)
}

func shouldDropDuplicatedBoundary(a, b string) bool {
	last := lastNonSpaceChar(a)
	first := firstNonSpaceChar(b)
	if last == 0 || last != first {
		return false
	}
	second := secondNonSpaceChar(b)
	return second != 0 && strings.ContainsRune(continuationPunctuation, second)
}

func isForcedContinuation(a, b string) bool {
	return startsWithContinuationPunctuation(b) || shouldDropDuplicatedBoundary(a, b)
}

func isOfficialDocumentTitleTail(text string) bool {
	t := strings.TrimSpace(text)
	for _, suf := range []string{"通知", "决定", "决议", "公告", "通告", "议案", "报告", "请示", "批复", "意见", "函", "纪要", "命令", "条例", "规定", "办法"} {
		if strings.HasSuffix(t, suf) && strings.Contains(t, "的") {
			return true
		}
	}
	return false
}

func isAddresseeSalutationLine(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	runes := []rune(t)
	last := runes[len(runes)-1]
	if last != '：' && last != ':' {
		return false
	}
	body := runes[:len(runes)-1]
	if len(body) < 1 || len(body) > 40 {
		return false
	}
	for _, r := range body {
		if !(isChineseRune(r) || r == '、' || r == '，' || r == ',' || r == ' ' || r == '\t' || r == '　') {
			return false
		}
	}
	return true
}

func isNumericHierarchyHardWrapContinuation(left, right string) bool {
	a := strings.TrimSpace(left)
	b := strings.TrimSpace(right)
	if a == "" || b == "" {
		return false
	}
	if !numericHierarchyPrefixDotRe.MatchString(a) {
		return false
	}
	rf := firstNonSpaceChar(b)
	if rf >= '0' && rf <= '9' {
		glued := a + b
		return IsStandaloneNumericHierarchyLine(glued) || isHeadingByRegex(glued)
	}
	if runeLen(b) <= 18 && !containsAnyRune(b, "，、；：,.!?;:") {
		return IsStandaloneNumericHierarchyLine(a + b)
	}
	return false
}

func shouldBlockLeftAlignedDateLineMerge(a, b TextBlock, cfg Config) bool {
	if !isStandaloneChineseDateLine(b.Text) {
		return false
	}
	leftDelta := b.Left - a.Left
	tol := cfg.XOffsetThresholdPt
	if leftDelta < -tol {
		return false
	}
	return leftDelta <= tol*3.0
}

func isShortLabel(text string) bool {
	return runeLen(strings.TrimSpace(text)) < 8
}

// hasTypographicBoundary / styleDifferent (same function, two names in the spec).
func styleDifferent(a, b TextBlock, cfg Config) bool {
	if math.Abs(a.FontSizeMean-b.FontSizeMean) > cfg.FontSizeDeltaPt {
		return true
	}
	if a.FontWeight != b.FontWeight {
		return true
	}
	return false
}

func isFullWidthBodyLine(line, next *TextBlock, pageRightEdge float64) bool {
	if line == nil || line.Bbox == nil || line.PageWidth <= 0 {
		return false
	}
	tolerance := fullWidthRightGapToleranceEm * math.Max(line.FontSizeMean, 1.0)
	if !math.IsNaN(pageRightEdge) && pageRightEdge > 0 {
		return boxURX(*line.Bbox) >= pageRightEdge-tolerance
	}
	rightGap := line.PageWidth - boxURX(*line.Bbox)
	nextLeft := line.Left
	if next != nil {
		nextLeft = next.Left
	}
	leftMarginEstimate := math.Max(math.Min(line.Left, nextLeft), 0.0)
	return rightGap <= leftMarginEstimate+tolerance
}

func isFullWidthHardWrapLeadLine(a, b *TextBlock, cfg Config, pageRightEdge float64) bool {
	if a == nil || b == nil {
		return false
	}
	if endsWithSentenceTerminator(a.Text) || endsWithSemanticBreak(a.Text) {
		return false
	}
	if isCenteredStructuralChapterHeading(*a) {
		return false
	}
	if styleDifferent(*a, *b, cfg) {
		return false
	}
	return isFullWidthBodyLine(a, b, pageRightEdge)
}

func layoutBreak(lastLine, b TextBlock, cfg Config, spacingMultiplier float64, paragraphContinuation bool, pageRightEdge float64) bool {
	indentChange := math.Abs(lastLine.IndentLeft - b.IndentLeft)
	spacing := math.Abs(b.TopDistance - lastLine.TopDistance)
	maxLine := math.Max(lastLine.LineHeight, 1.0)
	xOffset := math.Abs(lastLine.Left - b.Left)
	indentTolerance := cfg.IndentThresholdPt
	xTolerance := cfg.XOffsetThresholdPt
	if paragraphContinuation && lastLine.Bbox != nil && lastLine.PageWidth > 0 {
		if isFullWidthBodyLine(&lastLine, &b, pageRightEdge) {
			fontTolerance := paragraphContinuationXToleranceEm * math.Max(math.Max(lastLine.FontSizeMean, b.FontSizeMean), 1.0)
			indentTolerance = math.Max(indentTolerance, fontTolerance)
			xTolerance = math.Max(xTolerance, fontTolerance)
		} else {
			return true
		}
	}
	return indentChange > indentTolerance || spacing > maxLine*spacingMultiplier || xOffset > xTolerance
}

func isAcrossTable(a, b TextBlock) bool {
	if a.TableID < 0 && b.TableID < 0 {
		return false
	}
	return a.TableID != b.TableID
}

func isInlineTailContinuation(a, b TextBlock) bool {
	compact := strings.Join(strings.Fields(b.Text), "")
	if !inlineTailTokenRe.MatchString(compact) {
		return false
	}
	spacing := math.Abs(b.TopDistance - a.TopDistance)
	maxLine := math.Max(a.LineHeight, b.LineHeight)
	return spacing <= maxLine*2.5
}

func headingLastTopRef(t *TextBlock) float64 {
	if t == nil {
		return 0.0
	}
	if math.IsNaN(t.HeadingLastLineTop) {
		return t.TopDistance
	}
	return t.HeadingLastLineTop
}

func mergeText(a, b string) string {
	left, right := a, b
	if shouldDropDuplicatedBoundary(left, right) {
		r := []rune(right)
		right = string(r[1:])
	}
	if needSpace(left, right) {
		return left + " " + right
	}
	return left + right
}

// merge ports pdf-port/01 §merge.
func merge(a, b TextBlock, cfg Config) TextBlock {
	mergedText := mergeText(a.Text, b.Text)
	lastTop := math.Max(headingLastTopRef(&a), b.TopDistance)
	trailNorm := normalizeText(b.Text, cfg)
	out := a
	out.TopDistance = math.Min(a.TopDistance, b.TopDistance)
	out.Left = math.Min(a.Left, b.Left)
	out.Text = normalizeText(mergedText, cfg)
	out.FontSizeMean = (a.FontSizeMean + b.FontSizeMean) / 2.0
	out.FontWeight = maxInt(a.FontWeight, b.FontWeight)
	out.Italic = a.Italic || b.Italic
	out.MonoFont = a.MonoFont || b.MonoFont
	out.LineHeight = math.Max(a.LineHeight, b.LineHeight)
	out.IndentLeft = math.Min(a.IndentLeft, b.IndentLeft)
	out.HeadingLastLineTop = lastTop
	out.HeadingTrailingLeft = b.Left
	out.HeadingTrailingText = trailNorm
	out.HeadingPrefixStyleMismatch = a.HeadingPrefixStyleMismatch || b.HeadingPrefixStyleMismatch
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// shouldMerge ports pdf-port/01 §shouldMerge (rejection-first chain).
func shouldMerge(a, lastLine, b TextBlock, bodyFontMode float64, cfg Config, pageRightEdge float64) bool {
	na := a.WithText(normalizeText(a.Text, cfg))
	nb := b.WithText(normalizeText(b.Text, cfg))
	if strings.TrimSpace(na.Text) == "" || strings.TrimSpace(nb.Text) == "" {
		return false
	}
	if a.PageNo != b.PageNo {
		return false
	}
	chapterTitleContinuation := isChapterPrefixWithTitleNamePair(na.Text, nb.Text)
	if isCenteredStructuralChapterHeading(na) && !chapterTitleContinuation {
		return false
	}
	if !chapterTitleContinuation && shouldBlockMergeAtChapterHeadingBoundary(na, nb, cfg) {
		return false
	}
	if isOfficialDocumentTitleTail(na.Text) && isAddresseeSalutationLine(nb.Text) {
		return false
	}
	aList := IsListItem(na.Text)
	bList := IsListItem(nb.Text)
	listToContinuation := aList && !bList

	if isNumberedClauseContinuation(na.Text, nb.Text) {
		return true
	}
	if !endsWithSentenceTerminator(na.Text) && isBodyChapterReference(nb.Text) {
		return true
	}
	if isNumericHierarchyHardWrapContinuation(na.Text, nb.Text) {
		return true
	}
	if !listToContinuation && isHeading(na, bodyFontMode, cfg) && !isFullWidthHardWrapLeadLine(&na, &nb, cfg, pageRightEdge) {
		return false
	}
	if isHeading(nb, bodyFontMode, cfg) && !isBodyChapterReference(nb.Text) {
		return false
	}
	if listToContinuation && startsWithCnArticleHeadingExported(nb.Text) {
		return false
	}
	if (aList && bList) || (!aList && bList) {
		return false
	}
	if !listToContinuation && endsWithSentenceTerminator(na.Text) {
		return false
	}
	if !chapterTitleContinuation && !na.HeadingPrefixStyleMismatch && styleDifferent(na, nb, cfg) {
		return false
	}
	if isInlineTailContinuation(na, nb) {
		return true
	}
	paragraphContinuation := !endsWithSentenceTerminator(na.Text) && !isHeading(nb, bodyFontMode, cfg) && !IsListItem(nb.Text)
	spacingMultiplier := cfg.LineSpacingMultiplier
	if (paragraphContinuation || listToContinuation) && spacingMultiplier < 2.8 {
		spacingMultiplier = 2.8
	}
	ll := lastLine
	if layoutBreak(ll, nb, cfg, spacingMultiplier, paragraphContinuation, pageRightEdge) {
		return false
	}
	if isAcrossTable(na, nb) {
		return false
	}
	if shouldBlockLeftAlignedDateLineMerge(na, nb, cfg) {
		return false
	}
	if IsStandaloneNumericHierarchyLine(nb.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text) {
		return false
	}
	if IsStandaloneNumericHierarchyLine(na.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text) {
		return false
	}
	if isShortLabel(na.Text) && !endsWithSentenceTerminator(na.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text) {
		return false
	}
	if endsWithSemanticBreak(na.Text) {
		return false
	}
	if isForcedContinuation(na.Text, nb.Text) {
		return true
	}
	return true
}

// mergeLines ports pdf-port/01 §mergeLines.
func mergeLines(lines []TextBlock, bodyFontMode float64, cfg Config) []TextBlock {
	if len(lines) == 0 {
		return lines
	}
	pageRightEdge := estimatePageRightEdge(lines)
	var out []TextBlock
	i := 0
	for i < len(lines) {
		current := lines[i]
		lastLine := current
		for i+1 < len(lines) {
			next := lines[i+1]
			if shouldMerge(current, lastLine, next, bodyFontMode, cfg, pageRightEdge) {
				current = merge(current, next, cfg)
				lastLine = next
				i++
				continue
			}
			break
		}
		out = append(out, current)
		i++
	}
	return out
}

func estimatePageRightEdge(lines []TextBlock) float64 {
	maxURX := math.NaN()
	pageWidth := 0.0
	for _, l := range lines {
		if l.MonoFont || l.Bbox == nil {
			continue
		}
		if math.IsNaN(maxURX) || boxURX(*l.Bbox) > maxURX {
			maxURX = boxURX(*l.Bbox)
		}
		if l.PageWidth > pageWidth {
			pageWidth = l.PageWidth
		}
	}
	if !math.IsNaN(maxURX) && pageWidth > 0 && maxURX < pageWidth*0.6 {
		return math.NaN()
	}
	return maxURX
}

// mergeCrossPageTables ports pdf-port/01 §mergeCrossPageTables.
func mergeCrossPageTables(sorted []GeometricElement) []GeometricElement {
	var out []GeometricElement
	for _, el := range sorted {
		if len(out) > 0 {
			if t1, ok1 := isTableBlock(out[len(out)-1]); ok1 {
				if t2, ok2 := isTableBlock(el); ok2 &&
					t2.PageNo == t1.PageNo+1 && t2.ColCount == t1.ColCount &&
					t1.Bbox != nil && t2.Bbox != nil &&
					math.Abs(boxLLX(*t1.Bbox)-boxLLX(*t2.Bbox)) <= crossPageTableXTolerancePt &&
					math.Abs(boxURX(*t1.Bbox)-boxURX(*t2.Bbox)) <= crossPageTableXTolerancePt {
					out[len(out)-1] = appendTableRows(t1, t2)
					continue
				}
			}
		}
		out = append(out, el)
	}
	return out
}

func firstRowTextsEqual(t1, t2 TableBlock) bool {
	m1 := map[int]string{}
	m2 := map[int]string{}
	hasContent := false
	for _, c := range t1.Cells {
		if c.Row == 0 {
			m1[c.Col] = strings.TrimSpace(c.Text)
			if m1[c.Col] != "" {
				hasContent = true
			}
		}
	}
	for _, c := range t2.Cells {
		if c.Row == 0 {
			m2[c.Col] = strings.TrimSpace(c.Text)
			if m2[c.Col] != "" {
				hasContent = true
			}
		}
	}
	if len(m1) != len(m2) {
		return false
	}
	for k, v := range m1 {
		if m2[k] != v {
			return false
		}
	}
	return hasContent
}

func appendTableRows(t1, t2 TableBlock) TableBlock {
	skipRows := 0
	if firstRowTextsEqual(t1, t2) {
		skipRows = 1
	}
	cells := append([]TableCellData(nil), t1.Cells...)
	for _, c := range t2.Cells {
		if c.Row < skipRows {
			continue
		}
		cells = append(cells, TableCellData{
			Row: c.Row - skipRows + t1.RowCount, Col: c.Col,
			RowSpan: c.RowSpan, ColSpan: c.ColSpan, Text: c.Text,
		})
	}
	out := t1
	out.RowCount = t1.RowCount + t2.RowCount - skipRows
	out.Cells = cells
	return out
}

// demoteDecorativeSingleCellTables ports pdf-port/01's namesake.
func demoteDecorativeSingleCellTables(sorted []GeometricElement) []GeometricElement {
	out := append([]GeometricElement(nil), sorted...)
	i := 0
	for i < len(out) {
		if !isSingleCellTableCandidate(out[i]) {
			i++
			continue
		}
		end := i + 1
		for end < len(out) && isSingleCellTableCandidate(out[end]) {
			end++
		}
		convertRunIfDecorative(out, i, end)
		i = end
	}
	return out
}

func isSingleCellTableCandidate(e GeometricElement) bool {
	t, ok := isTableBlock(e)
	return ok && t.RowCount == 1 && t.ColCount == 1 && t.Bbox != nil
}

func convertRunIfDecorative(out []GeometricElement, start, end int) {
	if start-1 < 0 || end >= len(out) {
		return
	}
	before, ok1 := isTextBlock(out[start-1])
	after, ok2 := isTextBlock(out[end])
	if !ok1 || before.MonoFont || !ok2 || after.MonoFont {
		return
	}
	lineHeight := math.Max(before.LineHeight, after.LineHeight)
	if lineHeight <= 0 {
		return
	}
	maxHeight := lineHeight * decorativeSingleCellMaxLines
	for k := start; k < end; k++ {
		t, _ := isTableBlock(out[k])
		if t.Bbox == nil {
			return
		}
		if boxURY(*t.Bbox)-boxLLY(*t.Bbox) > maxHeight {
			return
		}
	}
	for k := start; k < end; k++ {
		t, _ := isTableBlock(out[k])
		converted := tableToTextBlock(t, before)
		if converted != nil {
			out[k] = *converted
		}
	}
}

func tableToTextBlock(table TableBlock, styleSource TextBlock) *TextBlock {
	text := ""
	if len(table.Cells) > 0 {
		text = table.Cells[0].Text
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return &TextBlock{
		baseElement: baseElement{
			ID:          table.ID, PageNo:      table.PageNo, Bbox:        table.Bbox,
			TopDistance: table.TopDistance, Left:        table.Left,
		},
		Text: text, FontSizeMean: styleSource.FontSizeMean, FontFamily: styleSource.FontFamily,
		FontWeight: styleSource.FontWeight, Italic: styleSource.Italic, MonoFont: false,
		LineHeight: styleSource.LineHeight, IndentLeft: table.Left, TableID: -1,
		BodyFontMode: styleSource.BodyFontMode,
		HeadingLastLineTop: math.NaN(), HeadingTrailingLeft: math.NaN(), HeadingTrailingText: "",
		PageWidth: styleSource.PageWidth, HeadingPrefixStyleMismatch: false,
	}
}

// mergeCrossPageParagraphBlocks ports pdf-port/01's namesake.
func mergeCrossPageParagraphBlocks(sorted []GeometricElement, cfg Config) []GeometricElement {
	var out []GeometricElement
	for _, el := range sorted {
		if len(out) > 0 {
			if t1, ok1 := isTextBlock(out[len(out)-1]); ok1 {
				if t2, ok2 := isTextBlock(el); ok2 && shouldMergeCrossPageParagraph(t1, t2, cfg) {
					out[len(out)-1] = merge(t1, t2, cfg)
					continue
				}
			}
		}
		out = append(out, el)
	}
	return out
}

func shouldMergeCrossPageParagraph(t1, t2 TextBlock, cfg Config) bool {
	if t2.PageNo != t1.PageNo+1 {
		return false
	}
	if t1.MonoFont || t2.MonoFont {
		return false
	}
	if t1.TableID >= 0 || t2.TableID >= 0 {
		return false
	}
	left := normalizeText(t1.Text, cfg)
	right := normalizeText(t2.Text, cfg)
	if left == "" || right == "" {
		return false
	}
	leftTail := left
	if strings.TrimSpace(t1.HeadingTrailingText) != "" {
		leftTail = t1.HeadingTrailingText
	}
	if endsWithSentenceTerminator(leftTail) || endsWithSemanticBreak(leftTail) {
		return false
	}
	if isHeadingByRegex(right) || IsListItem(right) {
		return false
	}
	if isHeading(t1.WithText(left), t1.BodyFontMode, cfg) {
		return false
	}
	if isStandaloneChineseDateLine(right) || isStandaloneChineseDateLine(left) {
		return false
	}
	if !t1.HeadingPrefixStyleMismatch && styleDifferent(t1, t2, cfg) {
		return false
	}
	return true
}

func startsWithCnArticleHeadingExported(text string) bool {
	return startsWithCnArticleHeading(text)
}

var (
	numericHierarchyPrefixDotRe = regexp.MustCompile(`^\d+(?:\.\d+)*\.$`)
	inlineTailTokenRe           = regexp.MustCompile(`^[A-Za-z0-9+\-_/().]{2,16}$`)
	standaloneChineseDateLineRe = regexp.MustCompile(`^\d{4}年\d{1,2}月\d{1,2}日$`)
)

func isStandaloneChineseDateLine(text string) bool {
	return standaloneChineseDateLineRe.MatchString(strings.TrimSpace(text))
}
