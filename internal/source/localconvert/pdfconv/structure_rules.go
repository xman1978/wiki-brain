package pdfconv

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MarkdownStructureRules port — facade methods only
// (pdf-port/04-toplevel-heuristics.md "MarkdownStructureRules" section).
// Per that section's "Go 移植提示", this is not kept as a literal facade
// class; the two functions docx-port/01 needs are exposed directly.

const terminalPunctuation = "。！？；：，、.,!?;:"

// EndsWithTerminalPunctuation ports MarkdownStructureRules.endsWithTerminalPunctuation.
func EndsWithTerminalPunctuation(text string) bool {
	if text == "" {
		return false
	}
	r := []rune(text)
	last := r[len(r)-1]
	return strings.ContainsRune(terminalPunctuation, last)
}

// IsChapterTableOfContentsEntry ports
// MarkdownStructureRules.isChapterTableOfContentsEntry (pure delegation to
// ChapterTocLineRemover.isChapterTocLine).
func IsChapterTableOfContentsEntry(line string) bool {
	return IsChapterTocLine(line)
}

// listBulletRe ports PdfToMarkdown.LIST_BULLET = "^[-+*•●○■□►→★☆]\s*.*"
// verbatim. Note this is broader than a "real" bullet marker: it matches
// even a bare marker character with no following text (Java .matches() on
// a no-newline single line makes "\s*.*" trivially satisfiable), and it
// includes ★/☆ which the project's prior placeholder omitted.
var listBulletRe = regexp.MustCompile(`^[-+*•●○■□►→★☆]`)

// listNumPrefixRe is the non-lookahead half of
// PdfToMarkdown.LIST_NUM_PREFIX's branch A:
// "\d+[.、)）\]]" (the "(?!\d)" is checked manually below — see
// matchListNumPrefixBody).
var listNumPrefixDigitBranchRe = regexp.MustCompile(`^\d+[.、)）\]]`)

// listNumPrefixParenBranchRe is LIST_NUM_PREFIX's branch B:
// "[（(]\s*\d+\s*[)）]" — no lookahead needed.
var listNumPrefixParenBranchRe = regexp.MustCompile(`^[（(]\s*\d+\s*[)）]`)

// matchListNumPrefixBody ports the matching half of
// PdfToMarkdown.LIST_NUM_PREFIX =
// "^(?:\d+[.、)）\]](?!\d)|[（(]\s*\d+\s*[)）])\s*(.*)$"
// (full-string match in Java; since Go's leading-anchor MatchString plus a
// captured "rest of string" tail is equivalent here, this returns the
// captured body (group 1) and whether the line matched at all).
func matchListNumPrefixBody(t string) (string, bool) {
	digitBranchOK := false
	if loc := listNumPrefixDigitBranchRe.FindStringIndex(t); loc != nil && loc[0] == 0 {
		// "(?!\d)": the char right after the separator must not be a digit
		// (avoids treating "98.5" as a list marker "98." + body "5").
		digitBranchOK = true
		if loc[1] < len(t) {
			r, _ := utf8.DecodeRuneInString(t[loc[1]:])
			if r >= '0' && r <= '9' {
				digitBranchOK = false
			}
		}
		if digitBranchOK {
			return strings.TrimLeft(t[loc[1]:], " \t　"), true
		}
	}
	if loc := listNumPrefixParenBranchRe.FindStringIndex(t); loc != nil && loc[0] == 0 {
		return strings.TrimLeft(t[loc[1]:], " \t　"), true
	}
	return "", false
}

// IsListItem ports PdfToMarkdown.isListItem literally.
//
// This is the real distinguishing rule between the two fixtures that
// motivated this port: LIST_BULLET/LIST_NUM_PREFIX only recognize a
// *digit* run (or a bullet-symbol) prefix — never a bare Latin letter.
// "a. 在域控制器上创建相关账户" therefore never reaches the list-item
// branches at all (TITLE_ALPHA is only consulted via isHeadingByRegex,
// which isListItem uses solely for the section-title-body *exception*, and
// only after a list-prefix already matched) and IsListItem correctly
// returns false for it. "1）支付条件：" matches LIST_NUM_PREFIX's branch A
// with body "支付条件："; that body fails looksLikeSectionTitleBody because
// it contains "：" (one of the sentence-punctuation characters), so the
// exception does not fire and IsListItem correctly returns true.
func IsListItem(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if IsLeadingPriorityMarkerHierarchyHeading(t) {
		return false
	}
	if listBulletRe.MatchString(t) {
		return true
	}
	rawBody, ok := matchListNumPrefixBody(t)
	if !ok {
		return false
	}
	body := normalizeListItemBodyText(rawBody)
	if body == "" {
		return false
	}
	// Avoid confusing short numbered section titles (e.g. "1. 总则") with
	// ordered lists.
	if isHeadingByRegex(t) && looksLikeSectionTitleBody(body) {
		return false
	}
	return true
}

// IsOrderedListItemLine ports MarkdownStructureRules.isOrderedListItemLine,
// which in the real Java source is a pure one-line delegation to
// PdfToMarkdown.isListItem (confirmed by reading MarkdownStructureRules.java
// — it is NOT a second, independently-implemented algorithm, despite the
// two living in different classes).
// IsOrderedListItemLine ports MarkdownStructureRules.isOrderedListItemLine,
// which in the real Java source is a pure one-line delegation to
// PdfToMarkdown.isListItem (confirmed by reading MarkdownStructureRules.java
// — it is NOT a second, independently-implemented algorithm, despite the
// two living in different classes).
func IsOrderedListItemLine(line string) bool {
	return IsListItem(line)
}

var (
	zeroWidthRe     = regexp.MustCompile(`[\x{200B}-\x{200D}\x{FEFF}]`)
	cjkSpaceRe      = regexp.MustCompile(`([\x{4e00}-\x{9fa5}])[ \t　]+([\x{4e00}-\x{9fa5}])`)
	multiSpaceRunRe = regexp.MustCompile(`[ \t　]{2,}`)
)

// normalizeListItemBodyText is a scoped port of PdfToMarkdown.normalizeText
// as used by isListItem: only the transforms that can plausibly affect a
// short list/heading body's emptiness, length, or punctuation content are
// ported (zero-width stripping, CJK-internal space collapsing, multi-space
// collapsing, trim). PDF-OCR-artifact repair steps from the real
// normalizeText — removeCharacterDoubling, mergeBrokenEnglishWords (needs a
// Config-provided common-short-word dictionary we do not have ported),
// mergeSingleDigitRuns, and the CJK/ASCII/digit boundary-spacing insertions
// — are intentionally not ported here: they exist to repair PDF text
// extraction artifacts (doubled glyphs, broken English tokens, digit runs
// split across text fragments) that do not occur in DOCX-sourced text, and
// none of them can change whether a short CJK phrase like "支付条件："
// trips the length/punctuation checks below. If a future fixture surfaces a
// case where this narrowing matters, this is the place to extend.
func normalizeListItemBodyText(text string) string {
	out := strings.TrimSpace(text)
	out = zeroWidthRe.ReplaceAllString(out, "")
	// Collapse whitespace runs between two CJK characters (repeated until
	// stable, since matches can overlap after a single pass, e.g. "招 标 文").
	for {
		next := cjkSpaceRe.ReplaceAllString(out, "$1$2")
		if next == out {
			break
		}
		out = next
	}
	out = multiSpaceRunRe.ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}
