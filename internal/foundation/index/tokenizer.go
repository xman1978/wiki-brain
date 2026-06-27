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
)

func init() {
	registry.RegisterTokenizer(TokenizerName, func(config map[string]interface{}, cache *registry.Cache) (analysis.Tokenizer, error) {
		return &gseTokenizer{}, nil
	})
}

// InitSegmenter loads the gse dictionary. Safe to call multiple times.
func InitSegmenter(dictFiles ...string) error {
	segOnce.Do(func() {
		if len(dictFiles) > 0 && dictFiles[0] != "" {
			segErr = seg.LoadDict(strings.Join(dictFiles, ","))
		} else {
			segErr = seg.LoadDict()
		}
		if segErr != nil {
			segErr = fmt.Errorf("gse: load dict: %w", segErr)
		}
	})
	return segErr
}

func ensureSegmenter() error {
	return InitSegmenter()
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
