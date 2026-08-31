package pdfconv

import (
	"regexp"
	"strings"
)

// HeadingSuppressHeuristics port (pdf-port/04-toplevel-heuristics.md
// "HeadingSuppressHeuristics" section) — only the subset transitively needed
// by HeadingPatternQualityHeuristics.clearlyFailsHeadingQuality, which the
// DOCX renderBodyBlocks path (docx-port/01 §5) depends on.

var (
	headingPrefixOnlyRe = regexp.MustCompile(
		`^(?:[（(][一二三四五六七八九十百千万]+[)）]|[一二三四五六七八九十百千万]+[、．.]|第\s*(?:[` +
			cnDigits + `]+)\s*(?:章|节|纲|目|条)|\d+(?:\.\d+)*[.、)）】]?|[（(]\s*\d+\s*[)）]|[IVXLCDMivxlcdm]+\.|[A-Za-z][.．])`)
	numericHierarchyPrefixRe = regexp.MustCompile(`^(\d+(?:\.\d+)*\.?)`)
	cnArticleHeadingRe       = regexp.MustCompile(`^第\s*[` + cnDigits + `]+\s*条`)
)

// IsStandaloneHeadingLine ports HeadingSuppressHeuristics.isStandaloneHeadingLine.
func IsStandaloneHeadingLine(line string) bool {
	if IsBlank(line) {
		return false
	}
	t := StripHeadingHashes(line)
	if t == "" {
		return false
	}
	loc := headingPrefixOnlyRe.FindStringIndex(t)
	if loc == nil || loc[0] != 0 {
		return false
	}
	rest := strings.TrimSpace(t[loc[1]:])
	if rest == "" {
		return true
	}
	if IsOrderedListItemLine(t) {
		return false
	}
	if containsAnyRune(rest, "。；") {
		return false
	}
	if runeLen(rest) > 50 {
		return false
	}
	return true
}

// IsStandaloneNumericHierarchyLine ports
// HeadingSuppressHeuristics.isStandaloneNumericHierarchyLine.
func IsStandaloneNumericHierarchyLine(line string) bool {
	if IsBlank(line) {
		return false
	}
	t := StripHeadingHashes(line)
	if t == "" || firstRuneAfter(t, 0) < '0' || firstRuneAfter(t, 0) > '9' {
		return false
	}
	if IsOrderedListItemLine(t) {
		return false
	}
	loc := numericHierarchyPrefixRe.FindStringIndex(t)
	if loc == nil || loc[0] != 0 {
		return false
	}
	rest := strings.TrimSpace(t[loc[1]:])
	if rest == "" {
		return true
	}
	if containsAnyRune(rest, "。；") {
		return false
	}
	return runeLen(rest) <= 18 && !containsAnyRune(rest, "，、；：,.!?;:")
}

func startsWithCnArticleHeading(text string) bool {
	if IsBlank(text) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(text))
	return cnArticleHeadingRe.MatchString(t) && !IsChapterTocLine(t)
}

func restAfterCnArticlePrefix(text string) string {
	if IsBlank(text) {
		return ""
	}
	t := strings.TrimSpace(text)
	loc := cnArticleHeadingRe.FindStringIndex(t)
	if loc == nil {
		return strings.TrimSpace(t)
	}
	return strings.TrimSpace(t[loc[1]:])
}

// LooksLikeCnArticleBodyParagraphLead ports
// HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead.
func LooksLikeCnArticleBodyParagraphLead(text string) bool {
	if IsBlank(text) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(text))
	if !startsWithCnArticleHeading(t) {
		return false
	}
	rest := restAfterCnArticlePrefix(t)
	if rest == "" {
		return false
	}
	if IsLikelyChapterTitleNameLine(rest) {
		return false
	}
	if containsAnyRune(rest, "，、") {
		return true
	}
	return runeLen(rest) > 18
}

// LooksLikeCnArticleBodySentence ports
// HeadingSuppressHeuristics.looksLikeCnArticleBodySentence.
func LooksLikeCnArticleBodySentence(text string) bool {
	if IsBlank(text) {
		return false
	}
	t := strings.TrimSpace(StripHeadingHashes(text))
	if !startsWithCnArticleHeading(t) {
		return false
	}
	if !strings.HasSuffix(t, "。") && !strings.HasSuffix(t, ".") {
		return false
	}
	return runeLen(restAfterCnArticlePrefix(t)) > 8
}

// isStandaloneCnArticleLine ports HeadingSuppressHeuristics.isStandaloneCnArticleLine.
func isStandaloneCnArticleLine(text string) bool {
	if IsBlank(text) {
		return false
	}
	t := StripHeadingHashes(text)
	if t == "" || !cnArticleHeadingRe.MatchString(t) {
		return false
	}
	if IsChapterTocLine(t) {
		return false
	}
	return IsStandaloneHeadingLine(text)
}

// prevEndsWithColon ports HeadingSuppressHeuristics.prevEndsWithColon.
func prevEndsWithColon(prevText string) bool {
	if IsBlank(prevText) {
		return false
	}
	t := strings.TrimSpace(prevText)
	return strings.HasSuffix(t, "：") || strings.HasSuffix(t, ":")
}

// looksLikeListGuideAnchor is the self-contained subset of
// ListGuideHeuristics.looksLikeListGuideAnchor needed by
// looksLikeListGuideContext below (pdf-port/04 "ListGuideHeuristics"
// section, ANCHOR_KEYWORDS table) — the scope-detection surface of
// ListGuideHeuristics (detectListGuideScopes / nonHeadingScopes) is out of
// this DOCX-scoped port (see heading_stage.go SCOPE NOTE); only the
// keyword-membership predicate is needed here.
var listGuideAnchorKeywords = []string{
	"包括下列内容", "包括以下内容", "包括以下部分", "包括以下", "下列部分",
	"如下", "如下所示", "下列内容", "下列章节", "以下章节", "如下章节",
	"参考下面的章节", "参考以下章节", "参考下列章节",
}

func looksLikeListGuideAnchor(normalizedLine string) bool {
	if IsBlank(normalizedLine) {
		return false
	}
	for _, kw := range listGuideAnchorKeywords {
		if strings.Contains(normalizedLine, kw) {
			return true
		}
	}
	return false
}

var numLevel2GuidePrefixRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

// looksLikeLevel2GuideAnchor ports ListGuideHeuristics.looksLikeLevel2GuideAnchor.
// The Java NUM_LEVEL2_PREFIX pattern has a negative lookahead
// `(?!\.)` after the second numeric group (rejecting "1.2.3"); Go's RE2
// lacks lookahead, so this is checked manually (see pdf-port "Go regexp
// 兼容性预警").
func looksLikeLevel2GuideAnchor(normalizedLine string) bool {
	if IsBlank(normalizedLine) {
		return false
	}
	if !strings.HasSuffix(normalizedLine, "：") && !strings.HasSuffix(normalizedLine, ":") {
		return false
	}
	loc := numLevel2GuidePrefixRe.FindStringSubmatchIndex(normalizedLine)
	if loc == nil || loc[0] != 0 {
		return false
	}
	// reject a third numeric segment ("1.2.3...") the way the Java
	// negative lookahead (?!\.) would.
	if loc[1] < len(normalizedLine) && normalizedLine[loc[1]] == '.' {
		return false
	}
	return true
}

// looksLikeListGuideContext ports HeadingSuppressHeuristics.looksLikeListGuideContext.
func looksLikeListGuideContext(prevText string) bool {
	if IsBlank(prevText) {
		return false
	}
	t := StripHeadingHashes(prevText)
	return looksLikeListGuideAnchor(t) || looksLikeLevel2GuideAnchor(t)
}

// shouldSuppressHeading ports HeadingSuppressHeuristics.shouldSuppressHeading
// (overload 1, pure-string). fontLikeBody is always false from the
// text-only shouldSuppressHeadingLine entry point (no font information in
// the DOCX/pure-Markdown-line pipeline).
func shouldSuppressHeading(text, prevText string, fontLikeBody bool) bool {
	if isBodyChapterReference(text) {
		return true
	}
	if IsChapterTableOfContentsEntry(text) {
		return true
	}
	if IsOrderedListItemLine(text) {
		return true
	}
	if isStandaloneCnArticleLine(text) {
		if prevEndsWithColon(prevText) && looksLikeListGuideContext(prevText) {
			return true
		}
		return false
	}
	if prevEndsWithColon(prevText) {
		if IsStructuralChapterHeading(text) && IsStandaloneHeadingLine(text) && !looksLikeListGuideContext(prevText) {
			return false
		}
		return true
	}
	if fontLikeBody && !IsStandaloneHeadingLine(text) {
		return true
	}
	return false
}

// previousNonBlankLine ports HeadingSuppressHeuristics.previousNonBlankLine.
func previousNonBlankLine(lines []string, fromLineID int) string {
	for i := fromLineID - 1; i >= 0; i-- {
		if i < 0 || i >= len(lines) {
			continue
		}
		if strings.TrimSpace(StripHeadingHashes(lines[i])) != "" {
			return lines[i]
		}
	}
	return ""
}

// looksLikeHeadingCandidate ports HeadingSuppressHeuristics.looksLikeHeadingCandidate.
func looksLikeHeadingCandidate(raw string) bool {
	if IsBlank(raw) {
		return false
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "#") {
		return true
	}
	t := StripHeadingHashes(raw)
	return t != "" && isHeadingByRegex(t)
}

// shouldSuppressHeadingLine ports HeadingSuppressHeuristics.shouldSuppressHeadingLine
// (the pure-Markdown-line-sequence entry point used by
// MarkdownHeadingStage.extractCandidates).
func shouldSuppressHeadingLine(lines []string, lineID int) bool {
	if lines == nil || lineID < 0 || lineID >= len(lines) {
		return false
	}
	raw := lines[lineID]
	if isBodyChapterReference(raw) {
		return true
	}
	if IsSectionTitleNumberedLine(StripHeadingHashes(raw)) {
		return false
	}
	if !looksLikeHeadingCandidate(raw) {
		return false
	}
	prev := previousNonBlankLine(lines, lineID)
	if shouldSuppressHeading(StripHeadingHashes(raw), prev, false) {
		return true
	}
	if !IsStandaloneHeadingLine(raw) {
		return true
	}
	return false
}
