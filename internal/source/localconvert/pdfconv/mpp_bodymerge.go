package pdfconv

import (
	"regexp"
	"strings"
	"unicode"
)

// mpp_bodymerge.go is a reduced-scope port of Part 6's MarkdownBodyMergeStage
// / MarkdownWeakMergeHeuristics (pdf-port/06-mpp-merge-cleanup.md) — the
// pipeline stage that runs AFTER titles are finalized (ApplyHeadingStage)
// and re-merges body lines that PDF page-width hard-wrapping split apart
// (e.g. a paragraph whose second physical line starts flush at the
// paragraph's left margin rather than continuing the first line). This
// closes a real, frequently-hit gap: Part 1's geometric shouldMerge
// (geometry_merge.go) makes its merge/no-merge call once, per page, from
// raw coordinates alone, and is conservative about hanging-indent gaps
// that turn out to be within the same paragraph; Part 6 gets a second,
// text-only pass at the same question with full-line context.
//
// What's ported: the core of mergeWrappedBodyLines's greedy merge loop,
// canMergeWrappedPair, joinWrappedLines, looksLikeWrappedTitleContinuation,
// and a reduced shouldBlockBodyMergePair.
//
// One deliberate departure from the Java source's blank-line handling:
// Java's mergeWrappedBodyLines treats "adjacent lines" and "lines separated
// by one blank line" as two different cases with different (the blank-line
// case much stricter — canMergeAcrossSingleBlankLine, requiring BOTH sides
// to be a short unpunctuated phrase) merge rules, because in the real
// pipeline a blank line is a meaningful signal: Part 2's renderer only
// inserts one between genuinely distinct paragraphs, so two lines with a
// blank line between them are presumptively different paragraphs unless
// they look like a short title fragment. render.go here does not carry that
// same signal — it inserts a blank line after every rendered block
// uniformly (see renderMarkdown's doc comment), including a wrapped
// paragraph continuation that Part 1's geometry conservatively left split.
// So here, both cases are treated identically via the SAME (less strict)
// canMergeWrappedPair/shouldBlockBodyMergePair rules — confirmed necessary
// against a real case (a "工具层：...混淆错误生成" / "工具等保证数据的质
// 量..." pair in a test document) where the hanging-indent gap (~40pt) is
// just barely outside Part 1's own tolerance (spec-value
// PARAGRAPH_CONTINUATION_X_TOLERANCE_EM=3.6em ≈ 37.6pt here) yet the
// reference output merges it, meaning the real pipeline's rescue path for
// this near-miss is Part 6 seeing it as an "adjacent" pair, not the
// stricter blank-line path.
//
// What's deliberately NOT ported (scope reduction, flagged the same way
// pipeline.go already flags the DOCX MPP-stage reduction):
//   - page-boundary-anchor handling (isPageBoundaryGap) — not meaningful
//     here since render.go's blank lines aren't tied to PDF page breaks;
//   - mergeWeakBodyLinesOutsideFences's separate 2-/3-line pass;
//   - attachment-list-scope and every OCR-specific heuristic
//     (isOcr*/shouldBlockGovernmentHeaderWeakMerge/
//     shouldBlockTripleUnpunctuatedPhrasePair) — OCR input is out of scope
//     for this whole port (see local-file-convert.md §6.5);
//   - splitGluedOrgLineAndChineseDateLine (a narrow OCR-glued-line repair);
//   - the sourceMarkdownHeadingLineIndexes / isMultiLineSourceAtxTitlePair
//     exemption (no PDF-specific "this # was in the original source, not
//     synthesized" distinction exists here the way it might for DOCX).

// closingPhrases is a small, well-known set of Chinese official-document
// closing idioms that are semantically complete standalone sentences
// despite carrying no terminal punctuation — see looksLikeDocumentClosingPhrase.
var closingPhrases = []string{"特此通知", "特此公告", "特此说明", "特此函复", "特此批复", "特此报告", "此致敬礼", "此致"}

func looksLikeDocumentClosingPhrase(s string) bool {
	t := strings.TrimSpace(s)
	for _, p := range closingPhrases {
		if t == p {
			return true
		}
	}
	return false
}

func isFenceLineForMerge(trimmed string) bool {
	return trimmed != "" && strings.HasPrefix(trimmed, "```")
}

func isTableLikeLineForMerge(trimmed string) bool {
	return classifyText(trimmed, nil, nil, nil, nil) == kindTable
}

func isQuoteOrRuleLineForMerge(trimmed string) bool {
	return classifyText(trimmed, nil, nil, nil, nil) == kindQuoteOrRule
}

func isListLikeLineForMerge(trimmed string) bool {
	return classifyText(trimmed, nil, nil, nil, nil) == kindListItem || isListLikeLineApprox(trimmed)
}

func isHeadingLikeLineForMerge(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if isBodyChapterReference(trimmed) {
		return false
	}
	if isMarkdownHeadingLine(trimmed) {
		return true
	}
	norm := NormalizeLine(trimmed)
	if norm == "" {
		return false
	}
	return MatchFirst(norm) != titlePatternNone
}

// isChineseDateLineForMerge ports MarkdownBodyMergeStage.isChineseDateLine.
func isChineseDateLineForMerge(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	return isStandaloneChineseDateLine(t) || fourDigitYearLeadRe.MatchString(t)
}

var fourDigitYearLeadRe = regexp.MustCompile(`^[0-9０-９]{4}年`)

func endsWithAsciiOrFullwidthColon(s string) bool {
	last := lastNonSpaceChar(s)
	return last == ':' || last == '：'
}

// endsWithTruncatedTimeColon ports the namesake: a trailing "H:" / "H："
// where the char before the colon is a digit (a clock time value cut off
// mid-render, e.g. "8:" continuing as "30"), not a real sentence/label end.
func endsWithTruncatedTimeColon(left, right string) bool {
	l := strings.TrimSpace(left)
	r := strings.TrimSpace(right)
	if l == "" || r == "" {
		return false
	}
	lr := []rune(l)
	last := lr[len(lr)-1]
	if last != ':' && last != '：' {
		return false
	}
	if len(lr) < 2 || !unicode.IsDigit(lr[len(lr)-2]) {
		return false
	}
	return unicode.IsDigit(firstNonSpaceChar(r))
}

var weakMergeClausePunct = "，、；,;"

func countWeakMergeClausePunct(s string) int {
	n := 0
	for _, r := range s {
		if strings.ContainsRune(weakMergeClausePunct, r) {
			n++
		}
	}
	return n
}

const tripleShortPhraseMaxLen = 40

// isShortUnpunctuatedPhrase ports the namesake.
func isShortUnpunctuatedPhrase(line string) bool {
	edge := strings.TrimSpace(line)
	if edge == "" {
		return false
	}
	if isFenceLineForMerge(edge) || isHeadingLikeLineForMerge(edge) || isListLikeLineForMerge(edge) ||
		isTableLikeLineForMerge(edge) || isQuoteOrRuleLineForMerge(edge) {
		return false
	}
	norm := NormalizeLine(edge)
	if norm == "" || runeLen(norm) > tripleShortPhraseMaxLen {
		return false
	}
	if EndsWithTerminalPunctuation(norm) {
		return false
	}
	punct := countWeakMergeClausePunct(norm)
	if punct >= 2 {
		return false
	}
	if runeLen(norm) >= 24 && punct >= 1 {
		return false
	}
	return true
}

var documentCoverTitleLineRe = regexp.MustCompile(`编制|审核|批准|技术有限公司|总经理办公会`)
var docSentenceTerminalRe = regexp.MustCompile(`[。！？]`)

// isDocumentCoverTitleMetadataLine ports the namesake (recognizes cover-page
// "编制：xxx" / "审核时间：xxx" style metadata lines).
func isDocumentCoverTitleMetadataLine(line string) bool {
	norm := NormalizeLine(strings.TrimSpace(line))
	if norm == "" {
		return false
	}
	if docSentenceTerminalRe.MatchString(norm) {
		return false
	}
	if strings.Contains(norm, "编制") && (strings.Contains(norm, "审核") || strings.Contains(norm, "批准") || strings.Contains(norm, "时间")) {
		return true
	}
	if strings.Contains(norm, "审核时间") || strings.Contains(norm, "批准时间") {
		return true
	}
	return documentCoverTitleLineRe.MatchString(norm)
}

// looksLikeWrappedTitleContinuation ports the namesake (reduced: drops the
// "印发"/organization-suffix special cases 3-4 and the OCR reference-number
// exemption in rule 11, since those are calibrated for OCR/government-notice
// input; keeps the general shape checks that matter for ordinary documents).
func looksLikeWrappedTitleContinuation(left, right string) bool {
	l := NormalizeLine(strings.TrimSpace(left))
	r := NormalizeLine(strings.TrimSpace(right))
	if l == "" || r == "" {
		return false
	}
	if EndsWithTerminalPunctuation(l) {
		return false
	}
	if endsWithAsciiOrFullwidthColon(r) && isShortUnpunctuatedPhrase(l) {
		return false
	}
	if coverFieldLabelLeadRe.MatchString(r) {
		return false
	}
	if isDocumentCoverTitleMetadataLine(r) && runeLen(r) < 72 && !strings.HasPrefix(r, "《") {
		return false
	}
	if strings.HasPrefix(r, "《") || strings.HasPrefix(r, "（") || strings.HasPrefix(r, "(") {
		return true
	}
	if strings.HasPrefix(l, "关于") && !strings.HasPrefix(r, "关于") && !strings.HasPrefix(r, "印发") && runeLen(l) >= 12 {
		return false
	}
	if runeLen(l) >= 12 {
		c := firstNonSpaceChar(r)
		return isChineseRune(c) || unicode.IsLetter(c) || unicode.IsDigit(c)
	}
	if runeLen(l) >= 4 {
		c := firstNonSpaceChar(r)
		orgLikeTail := strings.HasSuffix(l, "局") || strings.HasSuffix(l, "委") || strings.HasSuffix(l, "厅") ||
			strings.HasSuffix(l, "公司") || strings.HasSuffix(l, "中心") || strings.HasSuffix(l, "政府")
		return orgLikeTail && isChineseRune(c)
	}
	return false
}

var coverFieldLabelLeadRe = regexp.MustCompile(`^(?:时间|编制|审核|批准|准)\s*[：:]`)

// looksLikeBareTechnicalToken ports the namesake.
func looksLikeBareTechnicalToken(t string) bool {
	if strings.TrimSpace(t) == "" {
		return false
	}
	if cjkRatio(t) > 0 {
		return false
	}
	return !looksLikeProseLine(t)
}

// isListItemBodyContinuation ports the namesake: left is a "dangling
// enumeration lead" (contains 、/：/unclosed《 and no numbering pattern of
// its own) whose wrapped continuation right should be absorbed.
func isListItemBodyContinuation(left, right string) bool {
	if strings.TrimSpace(right) == "" {
		return false
	}
	l := NormalizeLine(left)
	r := NormalizeLine(right)
	if runeLen(l) < 18 {
		return false
	}
	if EndsWithTerminalPunctuation(l) {
		return false
	}
	hasUnclosedBookQuote := strings.Count(l, "《") > strings.Count(l, "》")
	if !strings.Contains(l, "：") && !strings.Contains(l, ":") && !hasUnclosedBookQuote && MatchFirst(l) != titlePatternNone {
		return false
	}
	if isListLikeLineForMerge(r) || isMarkdownHeadingLine(r) || isTableLikeLineForMerge(r) || isQuoteOrRuleLineForMerge(r) {
		return false
	}
	if lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(r) {
		return false
	}
	return strings.Contains(l, "：") || strings.Contains(l, ":") || strings.Contains(l, "，") || strings.Contains(l, "、")
}

func lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(line string) bool {
	edge := strings.TrimSpace(line)
	if edge == "" {
		return false
	}
	norm := NormalizeLine(edge)
	if norm != "" && MatchFirst(norm) != titlePatternNone {
		return true
	}
	return isListLikeLineApprox(edge)
}

// joinWrappedLines ports the namesake: plain concatenation, with a space
// inserted only when both boundary characters are non-CJK letters/digits.
func joinWrappedLines(current, next string) string {
	left := strings.TrimSpace(current)
	right := strings.TrimSpace(next)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	end := lastNonSpaceChar(left)
	begin := firstNonSpaceChar(right)
	addSpace := isAsciiLetterOrDigit(end) && isAsciiLetterOrDigit(begin) && !isChineseRune(end) && !isChineseRune(begin)
	if addSpace {
		return left + " " + right
	}
	return left + right
}

// mergeWrappedTitleContinuationLines ports the namesake: when both sides
// are "# " markdown headings, merge the bodies and keep the left level.
func mergeWrappedTitleContinuationLines(leftRaw, rightRaw string) string {
	left := strings.TrimSpace(leftRaw)
	right := strings.TrimSpace(rightRaw)
	lm := headingLineFullRe.FindStringSubmatch(left)
	rm := headingLineFullRe.FindStringSubmatch(right)
	if lm != nil && rm != nil {
		return lm[1] + " " + joinWrappedLines(lm[2], rm[2])
	}
	return joinWrappedLines(leftRaw, rightRaw)
}

// blocksClassifiedBodyMerge ports the namesake (reduced: kindHeading/
// kindTable/kindQuoteOrRule/kindPreformatted block merging; kindListItem
// blocks unless it's a body-continuation pair).
func blocksClassifiedBodyMerge(leftKind, rightKind lineKind, leftText, rightText string) bool {
	if leftKind == kindListItem && rightKind == kindNaturalText && isListItemBodyContinuation(leftText, rightText) {
		return false
	}
	if leftKind == kindHeading {
		if m := headingLineFullRe.FindStringSubmatch(strings.TrimSpace(leftText)); m != nil && rightKind == kindNaturalText {
			if isListItemBodyContinuation(m[2], rightText) {
				return false
			}
		}
	}
	blocks := func(k lineKind) bool {
		switch k {
		case kindHeading, kindTable, kindQuoteOrRule, kindPreformatted, kindListItem:
			return true
		}
		return false
	}
	return blocks(leftKind) || blocks(rightKind)
}

func canEnterBodyMerge(left, right string) bool {
	leftKind := classifyText(left, nil, nil, nil, nil)
	rightKind := classifyText(right, nil, nil, nil, nil)
	return !blocksClassifiedBodyMerge(leftKind, rightKind, left, right)
}

// shouldBlockBodyMergePair ports the namesake (reduced — see file doc
// comment for the specific rules dropped).
func shouldBlockBodyMergePair(left, right string, hierarchyLineIndexes map[int]bool, leftIdx, rightIdx int, protectedLeftTexts map[string]bool) bool {
	if isOfficialDocumentTitleTail(left) && isAddresseeSalutationLine(right) {
		return true
	}
	if protectedLeftTexts != nil && protectedLeftTexts[left] {
		// left was rendered from a PDF TextBlock whose font size/weight
		// genuinely differs from the block immediately following it (see
		// render.go's renderMarkdown) — e.g. a 35pt cover title followed by
		// a 14pt subtitle. By the time this text-only pass runs, that
		// geometry is gone, so render.go pre-computes which rendered lines
		// must never absorb their successor and passes their exact text
		// here (index-based tracking doesn't survive removeTocFromMarkdown,
		// which can delete lines earlier in the pipeline and shift every
		// index after it — text is stable across that).
		return true
	}
	leftIsHeadingLine := isMarkdownHeadingLine(left)
	rightIsHeadingLine := isMarkdownHeadingLine(right)
	if leftIsHeadingLine || rightIsHeadingLine {
		return true
	}
	if isListLikeLineForMerge(right) {
		return true
	}
	if hierarchyLineIndexes != nil && (hierarchyLineIndexes[leftIdx] || hierarchyLineIndexes[rightIdx]) {
		return true
	}
	// A line that reads as a genuine short title by numbering pattern
	// (isShortTitlePatternLine — same judgment isHeading uses at the
	// geometric layer, see its doc comment) but that ApplyHeadingStage
	// left un-promoted to "#" must never absorb, or be absorbed by, an
	// adjacent line — unconditionally, not just inside the
	// looksLikeWrappedTitleContinuation shape check below (a real
	// regression: "一、招待费用报销期限" doesn't match that shape check at
	// all — it's short and doesn't end in an org suffix — so without this
	// unconditional guard it fell through to classifyText's non-pattern-
	// aware classification and got glued onto the following paragraph).
	if (IsStructuralChapterHeading(left) || isShortTitlePatternLine(left)) && !isMarkdownHeadingLine(left) {
		return true
	}
	if (IsStructuralChapterHeading(right) || isShortTitlePatternLine(right)) && !isMarkdownHeadingLine(right) {
		return true
	}
	if looksLikeWrappedTitleContinuation(left, right) {
		leftKind := classifyText(left, nil, nil, nil, nil)
		rightKind := classifyText(right, nil, nil, nil, nil)
		if leftKind == kindPreformatted || rightKind == kindPreformatted {
			return true
		}
		if looksLikeBareTechnicalToken(left) || looksLikeBareTechnicalToken(right) {
			return true
		}
		leftCover := isDocumentCoverTitleMetadataLine(left)
		rightCover := isDocumentCoverTitleMetadataLine(right)
		if !leftCover && !rightCover {
			return false
		}
		// at least one side is cover metadata: fall through to further rules
	}
	leftKind := classifyText(left, nil, nil, nil, nil)
	rightKind := classifyText(right, nil, nil, nil, nil)
	if blocksClassifiedBodyMerge(leftKind, rightKind, left, right) {
		return true
	}
	leftCover := isDocumentCoverTitleMetadataLine(left)
	rightCover := isDocumentCoverTitleMetadataLine(right)
	if leftCover && rightCover {
		return true
	}
	if isChineseDateLineForMerge(right) || isChineseDateLineForMerge(left) {
		return true
	}
	return false
}

// canMergeWrappedPair ports the namesake (reduced).
func canMergeWrappedPair(current, next string, hierarchyLineIndexes map[int]bool, leftIdx, rightIdx int, protectedLeftTexts map[string]bool) bool {
	left := strings.TrimSpace(current)
	right := strings.TrimSpace(next)
	if left == "" || right == "" {
		return false
	}
	if isFenceLineForMerge(left) || isFenceLineForMerge(right) {
		return false
	}
	if looksLikeWrappedTitleContinuation(left, right) {
		return !shouldBlockBodyMergePair(left, right, hierarchyLineIndexes, leftIdx, rightIdx, protectedLeftTexts)
	}
	if !canEnterBodyMerge(left, right) {
		return false
	}
	if strings.HasPrefix(left, "关于") && runeLen(left) >= 12 {
		return false
	}
	if shouldBlockBodyMergePair(left, right, hierarchyLineIndexes, leftIdx, rightIdx, protectedLeftTexts) {
		return false
	}
	rightIsList := isListLikeLineForMerge(right)
	leftIsList := isListLikeLineForMerge(left)
	if rightIsList || (leftIsList && !isListItemBodyContinuation(left, right)) {
		return false
	}
	if isTableLikeLineForMerge(left) || isTableLikeLineForMerge(right) {
		return false
	}
	if isQuoteOrRuleLineForMerge(left) || isQuoteOrRuleLineForMerge(right) {
		return false
	}
	if EndsWithTerminalPunctuation(left) && !endsWithTruncatedTimeColon(left, right) {
		return false
	}
	if endsWithAsciiOrFullwidthColon(right) && isShortUnpunctuatedPhrase(left) {
		return false
	}
	if isChineseDateLineForMerge(right) || isChineseDateLineForMerge(left) {
		return false
	}
	first := firstNonSpaceChar(right)
	if !(isAsciiLetterOrDigit(first) || isChineseRune(first) || unicode.IsLetter(first)) {
		return false
	}
	return true
}

// mergeWrappedBodyLines ports the namesake's adjacent-line case (the
// cross-blank-line branch is dropped — see file doc comment).
func mergeWrappedBodyLines(lines []string, hierarchyLineIndexes map[int]bool, protectedLeftTexts map[string]bool) []string {
	var out []string
	inFence := false
	i := 0
	for i < len(lines) {
		current := lines[i]
		currentTrimmed := strings.TrimSpace(current)
		if isFenceLineForMerge(currentTrimmed) {
			inFence = !inFence
			out = append(out, current)
			i++
			continue
		}
		if inFence || currentTrimmed == "" {
			out = append(out, current)
			i++
			continue
		}
		merged := current
		cursor := i
		for {
			next := cursor + 1
			if next >= len(lines) {
				break
			}
			nextTrimmed := strings.TrimSpace(lines[next])
			if nextTrimmed == "" {
				// Bridge a single blank line (see file doc comment on why
				// this is treated the same as the adjacent case here,
				// unlike Java's stricter canMergeAcrossSingleBlankLine).
				// Never bridge a double blank line — that's the strongest
				// "these are unrelated" signal render.go ever emits.
				candidate := next + 1
				if candidate >= len(lines) || strings.TrimSpace(lines[candidate]) == "" {
					break
				}
				if looksLikeDocumentClosingPhrase(strings.TrimSpace(merged)) {
					// The real Java algorithm has a dedicated
					// looksLikeDistinctParagraphBoundaryAcrossBlank check
					// here (its body isn't given in pdf-port/06, so it
					// isn't ported) specifically to stop this class of
					// false merge: a short, punctuation-free closing idiom
					// ("特此通知"/"特此公告"...) is semantically complete
					// despite lacking terminal punctuation, and the blank
					// line after it is a real paragraph boundary (into a
					// signature block), not a hard-wrapped continuation —
					// unlike "工具层：..." which is a genuinely truncated
					// clause. Confirmed against a real regression: without
					// this check "特此通知" was being glued onto the
					// following "北京万户软件技术有限公司（免章）"
					// signature line.
					break
				}
				next = candidate
				nextTrimmed = strings.TrimSpace(lines[next])
			}
			mt := strings.TrimSpace(merged)
			if isChineseDateLineForMerge(nextTrimmed) || isChineseDateLineForMerge(mt) {
				break
			}
			if shouldBlockBodyMergePair(mt, nextTrimmed, hierarchyLineIndexes, i, next, protectedLeftTexts) {
				break
			}
			if !canMergeWrappedPair(merged, lines[next], hierarchyLineIndexes, i, next, protectedLeftTexts) {
				break
			}
			if looksLikeWrappedTitleContinuation(mt, nextTrimmed) {
				merged = mergeWrappedTitleContinuationLines(merged, lines[next])
			} else {
				merged = joinWrappedLines(merged, lines[next])
			}
			cursor = next
		}
		out = append(out, merged)
		i = cursor + 1
	}
	return out
}
