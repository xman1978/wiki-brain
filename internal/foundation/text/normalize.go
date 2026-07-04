// Package text provides shared question/term normalization used by both the
// Trace module (question_terms grouping) and the Activation module (matcher
// preprocessing and stored LinkCondition terms), so the two never drift into
// separate tokenization behavior (docs/impl/v1/activation.md 依赖节).
package text

import (
	"sort"
	"strings"
	"unicode"

	"github.com/jxman78/wiki-brain/internal/foundation/index"
)

var StopWords = map[string]bool{
	"的": true, "是": true, "吗": true, "了": true, "在": true, "有": true,
	"和": true, "与": true, "或": true, "不": true, "也": true, "都": true,
	"就": true, "这": true, "那": true, "被": true, "把": true, "让": true,
	"给": true, "从": true, "到": true, "对": true, "为": true, "以": true,
	"什么": true, "怎么": true, "哪些": true, "如何": true, "为何": true,
	"什": true, "么": true, "哪": true,
	"what": true, "is": true, "the": true, "a": true, "an": true,
	"how": true, "why": true, "where": true, "when": true, "who": true,
	"which": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"can": true, "could": true, "should": true, "shall": true, "may": true,
	"might": true, "must": true, "it": true, "its": true, "this": true,
	"that": true, "of": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "with": true, "by": true, "from": true,
	"and": true, "or": true, "not": true, "no": true,
}

// Normalize lowercases, trims, collapses whitespace runs to a single space,
// and drops everything that isn't a letter/digit/Han character. Word
// boundaries (single spaces) are preserved for the Tokenize step that
// typically follows.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var prev rune
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			if prev != ' ' {
				buf.WriteRune(' ')
				prev = ' '
			}
			continue
		}
		prev = r
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			buf.WriteRune(r)
		}
	}
	return strings.TrimSpace(buf.String())
}

// NormalizeCompact behaves like Normalize but strips whitespace entirely
// instead of collapsing it, for fields compared as a single opaque token
// rather than tokenized (e.g. activation_links.audience — see
// docs/impl/v1/activation.md 数据结构).
func NormalizeCompact(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// Terms tokenizes an already-normalized string, drops stop words, sorts the
// result, joins with spaces, and truncates to 200 chars.
func Terms(normalized string) string {
	words := Tokenize(normalized)

	var filtered []string
	for _, w := range words {
		if !StopWords[w] && w != "" {
			filtered = append(filtered, w)
		}
	}

	sort.Strings(filtered)
	result := strings.Join(filtered, " ")
	if len(result) > 200 {
		result = result[:200]
	}
	return result
}

// Tokenize splits s into CJK segments (via the gse segmenter) and Latin/digit
// runs, preserving their relative order.
func Tokenize(s string) []string {
	index.InitSegmenter()

	var cjkBuf strings.Builder
	var latBuf strings.Builder
	var tokens []string

	flush := func() {
		if cjkBuf.Len() > 0 {
			tokens = append(tokens, segmentCJK(cjkBuf.String())...)
			cjkBuf.Reset()
		}
		if latBuf.Len() > 0 {
			tokens = append(tokens, latBuf.String())
			latBuf.Reset()
		}
	}

	for _, r := range s {
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if unicode.Is(unicode.Han, r) {
			if latBuf.Len() > 0 {
				tokens = append(tokens, latBuf.String())
				latBuf.Reset()
			}
			cjkBuf.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if cjkBuf.Len() > 0 {
				tokens = append(tokens, segmentCJK(cjkBuf.String())...)
				cjkBuf.Reset()
			}
			latBuf.WriteRune(r)
			continue
		}
	}
	flush()
	return tokens
}

func segmentCJK(s string) []string {
	segments := index.Segment([]byte(s))
	var result []string
	for _, seg := range segments {
		text := strings.TrimSpace(seg)
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

// TermSet normalizes and tokenizes raw into a stop-word-filtered set, for
// Jaccard-style overlap scoring (docs/impl/v1/activation.md 步骤 2).
func TermSet(raw string) map[string]struct{} {
	return SplitTerms(Terms(Normalize(raw)))
}

// SplitTerms turns an already-normalized, space-joined terms string (as
// stored in DB columns like activation_links.subject_terms) into a set.
func SplitTerms(terms string) map[string]struct{} {
	set := make(map[string]struct{})
	if terms == "" {
		return set
	}
	for _, w := range strings.Split(terms, " ") {
		if w != "" {
			set[w] = struct{}{}
		}
	}
	return set
}
