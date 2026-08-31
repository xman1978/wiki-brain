package pdfconv

// Config mirrors FileView's PdfToMarkdown.Config defaults (pdf-port/01
// "Config 字段清单"), plus the Part 2 fields it references. Values are
// copied as-is from the documented defaults, not re-tuned.
type Config struct {
	YMergePt               float64
	FontSizeDeltaPt        float64
	IndentThresholdPt      float64
	XOffsetThresholdPt     float64
	LineSpacingMultiplier  float64
	TableOverlapRatio      float64
	HeaderTopRatio         float64
	FooterBottomRatio      float64
	HeaderPageNumberRatio  float64
	FooterPageNumberRatio  float64
	EmitTraceComments      bool
	EmitHeadingTrace       bool
	FallbackMergeMarkdown  bool
	MergeWrappedHeadings   bool
	RemoveToc              bool
	RemovePageNumbers      bool
	MaxHeadingLength       int
	HeadingMergeFontDeltaPt         float64
	HeadingMergeCenterTolerancePt   float64
	HeadingMergeMaxGapMultiplier    float64
	StyleClusterHeadingEnabled      bool
	ShortPhraseNumberedRunMin          int
	ShortPhraseNumberedRunMaxGap       int
	ShortPhraseNumberedRunMaxBodyLines int
	ShortPhraseNumberedRunSeqQualityMin float64
	ShortPhraseNumberedBodyMaxLen      int
	ShortStopwords map[string]struct{}
}

// DefaultConfig mirrors Config.defaults() (Java pdf-port/01, line 4495-4525).
func DefaultConfig() Config {
	return Config{
		YMergePt:              3.0,
		FontSizeDeltaPt:       0.5,
		IndentThresholdPt:     8.0,
		XOffsetThresholdPt:    10.0,
		LineSpacingMultiplier: 2.4,
		TableOverlapRatio:     0.15,
		HeaderTopRatio:        0.12,
		FooterBottomRatio:     0.88,
		HeaderPageNumberRatio: 0.12,
		FooterPageNumberRatio: 0.88,
		EmitTraceComments:     false,
		EmitHeadingTrace:      false,
		FallbackMergeMarkdown: true,
		MergeWrappedHeadings:  true,
		RemoveToc:             true,
		RemovePageNumbers:     true,
		MaxHeadingLength:      80,
		HeadingMergeFontDeltaPt:       1.2,
		HeadingMergeCenterTolerancePt: 24.0,
		HeadingMergeMaxGapMultiplier:  2.2,
		StyleClusterHeadingEnabled:    true,
		ShortPhraseNumberedRunMin:           3,
		ShortPhraseNumberedRunMaxGap:        3,
		ShortPhraseNumberedRunMaxBodyLines:  1,
		ShortPhraseNumberedRunSeqQualityMin: 0.8,
		ShortPhraseNumberedBodyMaxLen:       18,
		ShortStopwords: enShortStopwords,
	}
}

var enShortStopwords = map[string]struct{}{
	"a": {}, "an": {}, "am": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"do": {}, "go": {}, "he": {}, "i": {}, "in": {}, "is": {}, "it": {},
	"me": {}, "my": {}, "no": {}, "of": {}, "on": {}, "or": {}, "so": {},
	"to": {}, "up": {}, "us": {}, "we": {},
}

const (
	tableGeometryEpsilonPt         = 1.5
	crossPageTableXTolerancePt     = 6.0
	decorativeSingleCellMaxLines   = 2.5
	paragraphContinuationXToleranceEm = 3.6
	fullWidthRightGapToleranceEm   = 2.5
)
