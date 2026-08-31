package pdfconv

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HeadingPatternQualityHeuristics port
// (pdf-port/04-toplevel-heuristics.md "HeadingPatternQualityHeuristics"
// section) — the subset docx-port/01 §5 needs
// (DetectLineIndexesToDemoteAsNonHeading -> DetectDisqualifiedPatternKeys ->
// ClearlyFailsHeadingQuality).

const (
	prefixBodyLikeMinLen          = 35
	prefixBodyLikeMinPunct        = 2
	prefixBodyLikeMinPunctDensity = 0.015
	hardMaxNonSpaceLen            = 80
	incompleteTailMinLen          = 40
	// MaxHeadingLength mirrors loadMaxHeadingLength()'s default (80,
	// clamped to a minimum of 8). Java loads this from an external
	// config.properties; wiki-brain has no equivalent yet, so this stays a
	// package-level default (see pdf-port/04 §"Go 移植提示" 6).
	MaxHeadingLength = 80
)

var (
	cnParenColonGuideRe = regexp.MustCompile(`^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）][^：:]{0,40}[：:].*`)
)

const sentenceBoundaryPunct = "。；！？!?"
const clauseDensePunct = "，、,;；"

// isHeadingByRegex ports PdfToMarkdown.isHeadingByRegex literally. This is
// PdfToMarkdown.java's own set of TITLE_* constants (lines 62-71) — a
// DIFFERENT, smaller set of Pattern objects than
// HeadingLevelPrefixHeuristics's PREFIX_DEFS (ClassifyPrefixKey), even
// though several patterns look similar; the two classes are not
// interchangeable (confirmed by reading both real Java sources). This same
// function is also PdfToMarkdown.isListItem's direct dependency
// (isHeadingByRegex(t) && looksLikeSectionTitleBody(body)), so it must be
// literal for IsListItem's suppression-exception branch to be correct, not
// just for looksLikeHeadingCandidate's pre-filter use.
//
// Java: t.matches(pattern) requires the WHOLE trimmed line to match; since
// every pattern below ends in ".*" (or, for titleNumMultiRe, "$") and lines
// never contain "\n", "^prefix" + Go's MatchString is equivalent except for
// the two patterns with a negative lookahead, which get the
// find-then-check-boundary treatment used elsewhere in this package.
func isHeadingByRegex(norm string) bool {
	t := strings.TrimSpace(norm)
	if t == "" {
		return false
	}
	if titleCNNumRe.MatchString(t) {
		return true
	}
	if titleCNParenRe.MatchString(t) {
		return true
	}
	if titleChapterFullRe.MatchString(t) {
		return true
	}
	if titleNumSimpleMatches(t) {
		return true
	}
	if titleNumMultiMatches(t) {
		return true
	}
	if titleNumParenRe.MatchString(t) {
		return true
	}
	if titleNumSuffixRe.MatchString(t) {
		return true
	}
	if titleRomanRe.MatchString(t) {
		return true
	}
	if titleAlphaRe.MatchString(t) {
		return true
	}
	return false
}

// The following mirror PdfToMarkdown.java's TITLE_CN_NUM / TITLE_CN_PAREN /
// TITLE_CHAPTER / TITLE_NUM_PAREN / TITLE_NUM_SUFFIX / TITLE_ROMAN /
// TITLE_ALPHA constants (lines 62-71) verbatim, plus TITLE_NUM_SIMPLE /
// TITLE_NUM_MULTI's non-lookahead prefix (their negative lookaheads are
// checked manually below — Go RE2 has no lookahead support).
var (
	titleCNNumRe        = regexp.MustCompile(`^[一二三四五六七八九十百千万]+[、．.\s].*`)
	titleCNParenRe      = regexp.MustCompile(`^[（(][一二三四五六七八九十百千万]+[)）][、．.\s]?.*`)
	titleChapterFullRe  = regexp.MustCompile(`^第\s*([一二三四五六七八九十百千万零廿卅]+|\d+)\s*(章|节|纲|目|条).*`)
	titleNumSimplePfxRe = regexp.MustCompile(`^\d+[.．、，]`)
	titleNumMultiPfxRe  = regexp.MustCompile(`^\d+(?:\.\d+)+\.?`)
	titleNumParenRe     = regexp.MustCompile(`^[（(]\s*\d+\s*[)）]\s*.*`)
	titleNumSuffixRe    = regexp.MustCompile(`^\d+[)）】]\s*.*`)
	titleRomanRe        = regexp.MustCompile(`(?i)^[IVXLCDM]+\.\s*.*`)
	titleAlphaRe        = regexp.MustCompile(`^[A-Za-z]\.\s*.*`)
)

// titleNumSimpleMatches ports TITLE_NUM_SIMPLE = "^\d+[.．、，](?!\d)\s*.*".
func titleNumSimpleMatches(t string) bool {
	loc := titleNumSimplePfxRe.FindStringIndex(t)
	if loc == nil || loc[0] != 0 {
		return false
	}
	if loc[1] < len(t) {
		r, _ := utf8.DecodeRuneInString(t[loc[1]:])
		if unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// titleNumMultiMatches ports
// TITLE_NUM_MULTI = "^\d+(\.\d+)+\.?(?!\d|\.|\%|％).*$".
func titleNumMultiMatches(t string) bool {
	loc := titleNumMultiPfxRe.FindStringIndex(t)
	if loc == nil || loc[0] != 0 {
		return false
	}
	if loc[1] < len(t) {
		r, _ := utf8.DecodeRuneInString(t[loc[1]:])
		if unicode.IsDigit(r) || r == '.' || r == '％' || r == '%' {
			return false
		}
	}
	return true
}

func normalizeForScan(raw string) string {
	if raw == "" {
		return ""
	}
	t := strings.TrimSpace(raw)
	t = leadingHashPrefixRe.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "**") && strings.HasSuffix(t, "**") && runeLen(t) >= 4 {
		r := []rune(t)
		t = strings.TrimSpace(string(r[2 : len(r)-2]))
	}
	t = strings.ReplaceAll(t, " ", " ")
	return strings.TrimSpace(t)
}

func countNonSpaceChars(text string) int {
	n := 0
	for _, r := range text {
		if !isWhitespaceRune(r) {
			n++
		}
	}
	return n
}

func isWhitespaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '　':
		return true
	}
	return false
}

func countSentencePunctuation(text string) int {
	n := 0
	for _, r := range text {
		if strings.ContainsRune("，。；：、,.!?;:", r) {
			n++
		}
	}
	return n
}

func countCharsInSet(text, set string) int {
	n := 0
	for _, r := range text {
		if strings.ContainsRune(set, r) {
			n++
		}
	}
	return n
}

func hasMiddleChinesePeriod(text string) bool {
	idx := strings.Index(text, "。")
	if idx < 0 {
		return false
	}
	// "。" is a fixed 3-byte UTF-8 sequence, so this byte-offset comparison
	// correctly detects "not the final character".
	return idx < len(text)-len("。")
}

func isBodyLikeHeadingSentence(text string) bool {
	nonSpaceLen := countNonSpaceChars(text)
	if nonSpaceLen > hardMaxNonSpaceLen {
		return true
	}
	if nonSpaceLen < prefixBodyLikeMinLen {
		return false
	}
	punct := countSentencePunctuation(text)
	if punct < prefixBodyLikeMinPunct {
		return false
	}
	denom := nonSpaceLen
	if denom < 1 {
		denom = 1
	}
	density := float64(punct) / float64(denom)
	return density >= prefixBodyLikeMinPunctDensity
}

// isColonTerminatedSectionFieldLabel ports
// HeadingPatternQualityHeuristics.isColonTerminatedSectionFieldLabel.
func isColonTerminatedSectionFieldLabel(norm string) bool {
	if IsBlank(norm) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(norm))
	if !strings.HasSuffix(t, "：") && !strings.HasSuffix(t, ":") {
		return false
	}
	key := ClassifyPrefixKey(t)
	if key == "" {
		return false
	}
	switch key {
	case "TITLE_NUM_TOW", "TITLE_NUM_THREE", "TITLE_NUM_FOUR", "TITLE_NUM_FIVE",
		"TITLE_NUM_DOT", "TITLE_NUM_DUNHAO", "TITLE_NUM_SUFFIX", "TITLE_NUM_PAREN":
		return true
	}
	return false
}

// ClearlyFailsHeadingQuality ports
// HeadingPatternQualityHeuristics.clearlyFailsHeadingQuality (both overloads
// collapsed; maxHeadingLength defaults to MaxHeadingLength).
func ClearlyFailsHeadingQuality(text string) bool {
	return clearlyFailsHeadingQualityWithLen(text, MaxHeadingLength)
}

func clearlyFailsHeadingQualityWithLen(text string, maxHeadingLength int) bool {
	if IsBlank(text) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(text))
	if t == "" {
		return false
	}
	if IsChapterTocLine(t) {
		return false
	}
	if IsChapterTableOfContentsEntry(t) {
		return false
	}
	if LooksLikeCnArticleBodyParagraphLead(t) {
		return true
	}
	if LooksLikeCnArticleBodySentence(t) {
		return true
	}
	if strings.HasSuffix(t, "：") || strings.HasSuffix(t, ":") {
		return true
	}
	if cnParenColonGuideRe.MatchString(t) {
		return true
	}
	if hasMiddleChinesePeriod(t) {
		return true
	}
	if countNonSpaceChars(t) > hardMaxNonSpaceLen {
		return true
	}
	tLen := runeLen(t)
	sentenceEnds := countCharsInSet(t, sentenceBoundaryPunct)
	if tLen > maxHeadingLength && sentenceEnds >= 1 {
		return true
	}
	if tLen > maxHeadingLength*2 {
		return true
	}
	clauseCount := countCharsInSet(t, clauseDensePunct)
	if tLen > maxHeadingLength && clauseCount >= 4 {
		return true
	}
	if isBodyLikeHeadingSentence(t) {
		return true
	}
	if strings.HasSuffix(t, "及") && countNonSpaceChars(t) >= incompleteTailMinLen {
		return true
	}
	if !IsStandaloneHeadingLine(t) && ClassifyPrefixKey(t) != "" && countNonSpaceChars(t) > maxHeadingLength {
		return true
	}
	return false
}

func overlongPrefixTitleOnlyFailure(norm string, maxLen int) bool {
	if !clearlyFailsHeadingQualityWithLen(norm, maxLen) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(norm))
	if strings.HasSuffix(t, "：") || strings.HasSuffix(t, ":") {
		return false
	}
	if cnParenColonGuideRe.MatchString(t) {
		return false
	}
	if hasMiddleChinesePeriod(t) {
		return false
	}
	if isBodyLikeHeadingSentence(t) {
		return false
	}
	if LooksLikeCnArticleBodyParagraphLead(t) || LooksLikeCnArticleBodySentence(t) {
		return false
	}
	return ClassifyPrefixKey(t) != ""
}

// DetectDisqualifiedPatternKeys ports
// HeadingPatternQualityHeuristics.detectDisqualifiedPatternKeys.
func DetectDisqualifiedPatternKeys(lines []string) map[string]bool {
	result := map[string]bool{}
	if len(lines) == 0 {
		return result
	}
	maxLen := MaxHeadingLength
	for _, line := range lines {
		norm := normalizeForScan(line)
		if norm == "" {
			continue
		}
		pk := ClassifyPrefixKey(norm)
		if pk == "" {
			continue
		}
		if !isHeadingByRegex(norm) {
			continue
		}
		if isColonTerminatedSectionFieldLabel(norm) {
			continue
		}
		if overlongPrefixTitleOnlyFailure(norm, maxLen) {
			continue
		}
		if clearlyFailsHeadingQualityWithLen(norm, maxLen) {
			result[pk] = true
		}
	}
	return result
}

// countsForMixedRecognitionCandidate ports
// HeadingPatternQualityHeuristics.countsForMixedRecognitionCandidate.
func countsForMixedRecognitionCandidate(norm string) bool {
	if IsBlank(norm) {
		return false
	}
	if !isHeadingByRegex(norm) {
		return false
	}
	if IsChapterTocLine(norm) {
		return false
	}
	if IsChapterTableOfContentsEntry(norm) {
		return false
	}
	if isBodyChapterReference(norm) {
		return false
	}
	if isColonTerminatedSectionFieldLabel(norm) {
		return false
	}
	if IsOrderedListItemLine(norm) {
		return false
	}
	pk := ClassifyPrefixKey(norm)
	if pk == "" {
		return false
	}
	if LooksLikeSectionTitleNumberedLine(pk, norm, nil) {
		return true
	}
	switch pk {
	case "TITLE_CN_NUM", "TITLE_CN_PAREN", "TITLE_ROMAN", "TITLE_ALPHA":
		return IsStandaloneHeadingLine(norm)
	case "TITLE_CHAPTER_ONE", "TITLE_CHAPTER_TOW", "TITLE_CHAPTER_THREE", "TITLE_CHAPTER_FOUR", "TITLE_CHAPTER_FIVE":
		return true
	}
	return false
}

// detectMixedRecognitionPatternKeys ports
// HeadingPatternQualityHeuristics.detectMixedRecognitionPatternKeys (two-arg
// overload delegates here with excludedFromUnrecognizedCount = empty).
func detectMixedRecognitionPatternKeys(lines []string, headingLineIndexes map[int]bool, excludedFromUnrecognizedCount map[int]bool) map[string]bool {
	result := map[string]bool{}
	if len(lines) == 0 {
		return result
	}
	hasRecognized := map[string]bool{}
	hasUnrecognized := map[string]bool{}
	inFence := false
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		norm := normalizeForScan(raw)
		if !countsForMixedRecognitionCandidate(norm) {
			continue
		}
		recognized := headingLineIndexes != nil && headingLineIndexes[i]
		if recognized && clearlyFailsHeadingQualityWithLen(norm, MaxHeadingLength) {
			continue
		}
		pk := ClassifyPrefixKey(norm)
		if pk == "" {
			continue
		}
		excluded := excludedFromUnrecognizedCount != nil && excludedFromUnrecognizedCount[i]
		if recognized {
			if excluded {
				continue
			}
			hasRecognized[pk] = true
		} else {
			if excluded {
				continue
			}
			if !clearlyFailsHeadingQualityWithLen(norm, MaxHeadingLength) {
				hasUnrecognized[pk] = true
			}
		}
	}
	for pk := range hasRecognized {
		if hasUnrecognized[pk] {
			result[pk] = true
		}
	}
	// 2026-08-31 user decision (confirmed against a real FileView-converted
	// reference fixture, not a hypothesis): a standalone "中文数字、"
	// (Chinese-numeral + 顿号) prefixed line is always meant to be a
	// heading — countsForMixedRecognitionCandidate already requires
	// IsStandaloneHeadingLine for TITLE_CN_NUM evidence on either side of
	// this check, so every line counted here is already short and
	// well-formed on its own line, not a phrase merely embedded in body
	// prose. The scenario this was actually catching for TITLE_CN_NUM was a
	// real document that uses "一、二、三…" as its own top-level chapter
	// markers, where some later occurrences were copy-pasted without
	// reapplying the Word heading style (only visual bold/size, under the
	// 14pt boldAndLarge threshold) — connascence demoted the correctly
	// Word-styled siblings right along with the unstyled ones, discarding a
	// whole document's worth of top-level structure over one paragraph's
	// missing style. Exempting TITLE_CN_NUM here does not touch the
	// separate, still-active detectDisqualifiedPatternKeys quality gate
	// (long/punctuation-dense lines are still rejected there per-line), and
	// does not touch any other pattern key.
	delete(result, "TITLE_CN_NUM")
	return result
}

// buildInferDisqualifiedPatternKeys ports
// HeadingPatternQualityHeuristics.buildInferDisqualifiedPatternKeys (three-arg
// overload; two-arg delegates with excludedFromMixedCount = empty).
func buildInferDisqualifiedPatternKeys(lines []string, existingHeadings []*HeadingHit, excludedFromMixedCount map[int]bool) map[string]bool {
	keys := DetectDisqualifiedPatternKeys(lines)
	existingLines := map[int]bool{}
	for _, h := range existingHeadings {
		if h.LineIndex >= 0 {
			existingLines[h.LineIndex] = true
		}
	}
	for pk := range detectMixedRecognitionPatternKeys(lines, existingLines, excludedFromMixedCount) {
		keys[pk] = true
	}
	return keys
}

// filterHitsAndDemoteLines ports HeadingPatternQualityHeuristics.filterHitsAndDemoteLines
// (catalog-aware overload 2 collapsed: catalog cross-validation is out of
// this DOCX-scoped port's SCOPE NOTE, so the catalog exemption branch never
// fires here).
func filterHitsAndDemoteLines(lines []string, hits []*HeadingHit, disqualifiedPatternKeys map[string]bool) []*HeadingHit {
	if len(disqualifiedPatternKeys) == 0 {
		return hits
	}
	if len(lines) != 0 {
		demoteDisqualifiedPatternLines(lines, disqualifiedPatternKeys)
	}
	if len(hits) == 0 {
		return nil
	}
	var out []*HeadingHit
	for _, h := range hits {
		pk := ClassifyPrefixKey(StripHeadingHashes(h.TitleRaw))
		if pk != "" && disqualifiedPatternKeys[pk] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// demoteDisqualifiedPatternLines ports
// HeadingPatternQualityHeuristics.demoteDisqualifiedPatternLines (catalog-aware
// overload 2 collapsed, same rationale as filterHitsAndDemoteLines above).
func demoteDisqualifiedPatternLines(lines []string, disqualifiedPatternKeys map[string]bool) {
	if len(lines) == 0 || len(disqualifiedPatternKeys) == 0 {
		return
	}
	inFence := false
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		norm := normalizeForScan(raw)
		if norm == "" {
			continue
		}
		pk := ClassifyPrefixKey(norm)
		if pk == "" || !disqualifiedPatternKeys[pk] {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if m := headingLineRe.FindStringSubmatch(trimmed); m != nil {
				lines[i] = NormalizeLine(m[2])
			}
		}
	}
}

// DetectLineIndexesToDemoteAsNonHeading ports
// HeadingPatternQualityHeuristics.detectLineIndexesToDemoteAsNonHeading.
func DetectLineIndexesToDemoteAsNonHeading(lines []string, isCurrentlyHeading func(int) bool) map[int]bool {
	bad := DetectDisqualifiedPatternKeys(lines)
	result := map[int]bool{}
	if len(bad) == 0 {
		return result
	}
	for i, line := range lines {
		if !isCurrentlyHeading(i) {
			continue
		}
		pk := ClassifyPrefixKey(normalizeForScan(line))
		if pk != "" && bad[pk] {
			result[i] = true
		}
	}
	return result
}
