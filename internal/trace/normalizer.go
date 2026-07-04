package trace

import (
	"crypto/sha256"
	"fmt"

	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// normalize, tokenize and questionTerms delegate to the shared foundation/text
// package (also used by internal/activation) so the two never drift into
// separate tokenization behavior (docs/impl/v1/activation.md 依赖节).

func normalize(question string) string {
	return text.Normalize(question)
}

func questionHash(normalized string) string {
	h := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", h[:16])
}

func questionTerms(normalized string) string {
	return text.Terms(normalized)
}

func tokenize(s string) []string {
	return text.Tokenize(s)
}
