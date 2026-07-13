package unit

import (
	"unicode"

	"github.com/jxman78/wiki-brain/internal/foundation/index"
)

// tokenOverlap returns the containment coefficient (shared / min(|A|,|B|))
// of a and b's gse word-segmented token sets — the same tokenizer bleve
// indexing already uses, so this adds no new dependency. Deliberately not
// Jaccard (shared / union): the flagship case this gate exists for is a
// short heading immediately followed by its own long content
// (docs/impl/mvp/unit.md 3.3), and Jaccard's union-sized denominator lets
// the long side's vocabulary swamp the heading's few tokens — a real
// duplicate pair like "(二)年度积分基准线" + its paragraph scored 0.09
// Jaccard (below any sane threshold) but 0.5 containment, since almost all
// of the heading's tokens do appear in the content. Containment asks "does
// the smaller side's vocabulary show up in the larger side", which is what
// this gate actually needs to ask.
//
// This only ever answers "is there essentially no shared vocabulary" —
// callers use a low score to skip an LLM call, never a high one to
// auto-confirm a duplicate, since only the model can judge whether two
// spans describe the same fact.
func tokenOverlap(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}

	shared := 0
	for tok := range setA {
		if setB[tok] {
			shared++
		}
	}

	minLen := len(setA)
	if len(setB) < minLen {
		minLen = len(setB)
	}
	return float64(shared) / float64(minLen)
}

// tokenCount returns the number of unique gse tokens in text — used to spot
// a short lead-in (a heading or a code comment) whose overlap score against
// its content isn't trustworthy either way, not just when it's low: a
// Chinese comment above an English/SQL command can share zero tokens with
// the command even when it's unambiguously that command's own lead-in.
func tokenCount(text string) int {
	return len(tokenSet(text))
}

// tokenSet keeps only tokens with at least one letter or digit — gse hands
// back bare punctuation ("-", ";", "(", ")") as its own tokens, which would
// otherwise inflate both the overlap score and, more importantly,
// tokenCount's short-lead-in detection (a 1-3-word heading wrapped in
// parens/dashes reads as 5+ raw tokens without this filter).
func tokenSet(text string) map[string]bool {
	tokens := index.Segment([]byte(text))
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		if !hasLetterOrDigit(t) {
			continue
		}
		set[t] = true
	}
	return set
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
