package trace

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/jxman78/wiki-brain/internal/foundation/index"
)

var stopWords = map[string]bool{
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

func normalize(question string) string {
	s := strings.ToLower(strings.TrimSpace(question))
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

func questionHash(normalized string) string {
	h := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", h[:16])
}

func questionTerms(normalized string) string {
	words := tokenize(normalized)

	var filtered []string
	for _, w := range words {
		if !stopWords[w] && w != "" {
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

func tokenize(s string) []string {
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
