// Package pdfconv is a logic-to-logic port of the pure-text/string heuristic
// classes shared by FileView's PDF and Word (WordToMarkdown.java) Markdown
// conversion pipelines. Only the subset actually needed by the DOCX port
// (docs/impl/v1/docx-port/01-word-to-markdown.md) is implemented here — see
// docs/impl/v1/pdf-port/04-toplevel-heuristics.md, 05-mpp-heading-stack.md,
// 06-mpp-merge-cleanup.md for the full first-hand algorithm specs.
//
// This file holds primitives shared across the ported classes: line
// normalization, and the numbered-prefix parsing helpers used by both
// HeadingSequenceConsistencyHeuristics and ShortPhraseListRunHeuristics
// (docs/impl/v1/pdf-port/04-toplevel-heuristics.md "Go 包组织建议" §2).
package pdfconv

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsBlank reports whether s is empty or consists only of whitespace
// (including the fullwidth space U+3000, which unicode.IsSpace already
// covers — see pdf-port/04 §"Go regexp 兼容性预警" final bullet).
func IsBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}

var (
	leadingMDHashRe  = regexp.MustCompile(`^\s{0,3}#{1,6}[\s\x{3000}]*`)
	trailingMDHashRe = regexp.MustCompile(`[\s\x{3000}]*#{1,}[\s\x{3000}]*$`)
	spaceRunRe       = regexp.MustCompile(`[\s\x{3000}]+`)
)

// StripHeadingHashes mirrors ChapterReferenceHeuristics.stripHashes /
// HeadingSuppressHeuristics.stripHashes (identical logic in both Java
// classes; ported once here per the shared-helper recommendation).
func StripHeadingHashes(line string) string {
	if IsBlank(line) {
		return ""
	}
	t := strings.TrimSpace(line)
	t = leadingMDHashRe.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}

// NormalizeLine ports MarkdownPipelineLineUtils.normalizeLine
// (pdf-port/05 §MarkdownPipelineLineUtils), which is also the canonical
// implementation ListGuideHeuristics/HeadingLevelPrefixHeuristics call.
func NormalizeLine(line string) string {
	t := strings.TrimSpace(line)
	t = leadingMDHashRe.ReplaceAllString(t, "")
	t = trailingMDHashRe.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	t = spaceRunRe.ReplaceAllString(t, " ")
	return t
}

// numberedEntry ports the shared (patternKey, index[]) record used by
// HeadingSequenceConsistencyHeuristics.parseSequenceLine and
// ShortPhraseListRunHeuristics.parseNumberedLine.
type numberedEntry struct {
	LineID     int
	NormText   string
	PatternKey string
	Index      []int
}

// SplitDotInts ports HeadingSequenceConsistencyHeuristics.splitDotInts /
// ShortPhraseListRunHeuristics.splitDotInts (identical private methods).
func SplitDotInts(dotted string) []int {
	parts := strings.Split(dotted, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

var chineseDigits = map[rune]int{
	'零': 0, '一': 1, '二': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

var chineseUnits = map[rune]int{
	'十': 10, '百': 100, '千': 1000,
}

// ParseChineseNumber ports the standard Chinese-numeral-to-int algorithm
// described in HeadingSequenceConsistencyHeuristics.parseChineseNumber.
func ParseChineseNumber(token string) (int, bool) {
	total, section, number := 0, 0, 0
	any := false
	for _, r := range token {
		if d, ok := chineseDigits[r]; ok {
			number = d
			any = true
			continue
		}
		if r == '万' {
			if number == 0 {
				number = 1
			}
			section = (section + number) * 10000
			total += section
			section = 0
			number = 0
			any = true
			continue
		}
		if u, ok := chineseUnits[r]; ok {
			if number == 0 {
				number = 1
			}
			section += number * u
			number = 0
			any = true
			continue
		}
		// non chinese-numeral char: ignore (filtered per Java semantics)
	}
	result := total + section + number
	if !any || result <= 0 {
		return 0, false
	}
	return result, true
}

var romanValues = map[rune]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}

// ParseRoman ports HeadingSequenceConsistencyHeuristics.parseRoman.
func ParseRoman(token string) (int, bool) {
	t := strings.ToUpper(token)
	runes := []rune(t)
	total, prev := 0, 0
	for i := len(runes) - 1; i >= 0; i-- {
		v, ok := romanValues[runes[i]]
		if !ok {
			return 0, false
		}
		if v < prev {
			total -= v
		} else {
			total += v
			prev = v
		}
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// IsSequentialIndex ports HeadingSequenceConsistencyHeuristics.isSequentialIndex
// / ShortPhraseListRunHeuristics.isSequential (identical algorithm).
func IsSequentialIndex(a, b []int) bool {
	if a == nil || b == nil || len(a) != len(b) || len(a) == 0 {
		return false
	}
	for k := 0; k < len(a)-1; k++ {
		if a[k] != b[k] {
			return false
		}
	}
	return b[len(b)-1] == a[len(a)-1]+1
}

// numericBoundaryOK implements the negative-lookahead replacement described
// in pdf-port/04's "Go regexp 兼容性预警" table: after matching a numeric
// prefix up to matchEnd (byte offset into s), check that the following rune
// is not one of the disallowed boundary characters.
func numericBoundaryOK(s string, matchEnd int, disallowDotOrDash bool) bool {
	if matchEnd >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[matchEnd:])
	if unicode.IsDigit(r) {
		return false
	}
	if disallowDotOrDash && (r == '.' || r == '．' || r == '-') {
		return false
	}
	if !disallowDotOrDash && r == '-' {
		return false
	}
	return true
}

// firstRuneAfter returns the rune at byte offset idx in s, or 0 if idx is
// out of range. Used by several boundary/edge checks below.
func firstRuneAfter(s string, idx int) rune {
	if idx < 0 || idx >= len(s) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s[idx:])
	return r
}

// strings_TrimSpace is strings.TrimSpace under a distinct name to avoid a
// stray unused-import edge case in generated call sites; behaves identically.
func strings_TrimSpace(s string) string { return strings.TrimSpace(s) }

// runeLen returns the length of s in runes (Go's analogue of Java's
// codePointCount — see pdf-port/04 §6.2 "(6)" on UTF-16 vs rune semantics).
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// containsAnyRune reports whether s contains any rune from chars.
func containsAnyRune(s, chars string) bool {
	return strings.ContainsAny(s, chars)
}

// containsDigit reports whether s contains any ASCII digit.
func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
