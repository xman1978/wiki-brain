package index

import (
	"fmt"
	"strings"
	"sync"

	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/registry"
	"github.com/go-ego/gse"
)

const TokenizerName = "gse"

var (
	seg     gse.Segmenter
	segOnce sync.Once
	segErr  error

	baseSeg     gse.Segmenter
	baseSegOnce sync.Once
	baseSegErr  error
)

func init() {
	registry.RegisterTokenizer(TokenizerName, func(config map[string]interface{}, cache *registry.Cache) (analysis.Tokenizer, error) {
		return &gseTokenizer{}, nil
	})
}

// InitSegmenter loads the gse dictionary. Safe to call multiple times (only
// the first call's dictFiles take effect — sync.Once).
//
// gse.LoadDict(files...) is additive only when called more than once: the
// first call with no arguments loads gse's bundled base dictionary; a
// **second** call with files replaces nothing, it just reads those files into
// the already-initialized Dict (LoadDict only skips the base-dict load when
// len(files) > 0, see dict_util.go). Calling it once with custom files and
// never with no args — which is what this function used to do — silently
// segments on the custom vocabulary alone, splitting nearly every ordinary
// word into single characters (discovered via 达梦/会话 both shattering into
// individual Han characters in production). Base-then-custom is required.
func InitSegmenter(dictFiles ...string) error {
	segOnce.Do(func() {
		if segErr = seg.LoadDict(); segErr != nil {
			segErr = fmt.Errorf("gse: load base dict: %w", segErr)
			return
		}
		if len(dictFiles) > 0 && dictFiles[0] != "" {
			if err := seg.LoadDict(strings.Join(dictFiles, ",")); err != nil {
				segErr = fmt.Errorf("gse: load custom dict: %w", err)
			}
		}
	})
	return segErr
}

func ensureSegmenter() error {
	return InitSegmenter()
}

// ensureBaseSegmenter loads gse's bundled base dictionary only, without any
// of the domain-specific compound entries InitSegmenter's dictFiles add. Used
// by ExpandedTokens to recover sub-tokens that a domain compound word (e.g.
// "索引优化" in config/dict/it.txt) would otherwise swallow at query time,
// causing a query for "达梦数据库怎么优化索引" to segment "索引" away into a
// compound token that never matches documents where "索引" appears on its
// own (see P13 test finding 2026-08-26).
func ensureBaseSegmenter() error {
	baseSegOnce.Do(func() {
		baseSegErr = baseSeg.LoadDict()
	})
	return baseSegErr
}

// segmentBase splits input bytes using gse's base dictionary only (no
// domain-specific compound entries).
func segmentBase(input []byte) []string {
	ensureBaseSegmenter()
	segments := baseSeg.Segment(input)
	result := make([]string, 0, len(segments))
	for _, s := range segments {
		text := s.Token().Text()
		if text != "" && text != " " && text != "\n" && text != "\t" {
			result = append(result, text)
		}
	}
	return result
}

// ExpandedTokens returns the union of the full-dictionary (base + domain
// compounds) and base-dictionary-only segmentations of text, deduplicated,
// preserving first-seen order. Query-side use only — it recovers sub-tokens
// that a domain compound dictionary entry would otherwise hide, so a query
// containing "索引优化" still surfaces documents where "索引" appears alone.
// Not used at index time: documents keep their normal full-dictionary
// segmentation, so this only widens what a query can match, it never changes
// what gets indexed.
func ExpandedTokens(text string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, t := range Segment([]byte(text)) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range segmentBase([]byte(text)) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Segment splits input bytes into word tokens using gse.
func Segment(input []byte) []string {
	ensureSegmenter()
	segments := seg.Segment(input)
	result := make([]string, 0, len(segments))
	for _, s := range segments {
		text := s.Token().Text()
		if text != "" && text != " " && text != "\n" && text != "\t" {
			result = append(result, text)
		}
	}
	return result
}

type gseTokenizer struct{}

func (t *gseTokenizer) Tokenize(input []byte) analysis.TokenStream {
	segments := seg.Segment(input)
	tokens := make(analysis.TokenStream, 0, len(segments))
	pos := 1
	for _, s := range segments {
		token := s.Token().Text()
		if token == "" || token == " " || token == "\n" || token == "\t" {
			continue
		}
		tokens = append(tokens, &analysis.Token{
			Term:     []byte(token),
			Start:    s.Start(),
			End:      s.End(),
			Position: pos,
			Type:     analysis.Ideographic,
		})
		pos++
	}
	return tokens
}
