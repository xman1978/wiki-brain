package pdfconv

import (
	"math"

	"github.com/ivanvanderbyl/docmill/v2/pkg/geom"
	"github.com/ivanvanderbyl/docmill/v2/pkg/page"
)

// GeometricElement is the common view shared by TextBlock and TableBlock,
// mirroring Java's abstract GeometricElement (pdf-port/01 §数据结构).
type GeometricElement interface {
	ElemID() string
	ElemPageNo() int
	ElemBBox() *geom.Box
	ElemTopDistance() float64
	ElemLeft() float64
}

// baseElement's fields are exported to match the spec's direct field
// notation (a.TopDistance, b.Left, etc.) used throughout the ported
// algorithm pseudocode.
type baseElement struct {
	ID          string
	PageNo      int
	Bbox        *geom.Box
	TopDistance float64
	Left        float64
}

func (b baseElement) ElemID() string           { return b.ID }
func (b baseElement) ElemPageNo() int          { return b.PageNo }
func (b baseElement) ElemBBox() *geom.Box      { return b.Bbox }
func (b baseElement) ElemTopDistance() float64 { return b.TopDistance }
func (b baseElement) ElemLeft() float64        { return b.Left }

// TextBlock corresponds to Java's TextBlock: one visual line, or several
// merged into a paragraph.
type TextBlock struct {
	baseElement

	Text         string
	FontSizeMean float64
	FontFamily   string
	FontWeight   int
	Italic       bool
	MonoFont     bool
	LineHeight   float64
	IndentLeft   float64
	TableID      int
	BodyFontMode float64

	HeadingLastLineTop         float64
	HeadingTrailingLeft        float64
	HeadingTrailingText        string
	PageWidth                  float64
	HeadingPrefixStyleMismatch bool
}

// WithText returns a copy with Text replaced (Java TextBlock.withText).
func (t TextBlock) WithText(newText string) TextBlock {
	t.Text = newText
	return t
}

// TableCellData corresponds to Java's TableCellData record.
type TableCellData struct {
	Row, Col         int
	RowSpan, ColSpan int
	Text             string
}

// TableBlock corresponds to Java's TableBlock.
type TableBlock struct {
	baseElement

	RowCount, ColCount int
	Cells              []TableCellData
	SingleCellLines    []string // non-nil only for 1x1 tables
}

func isTextBlock(e GeometricElement) (TextBlock, bool) {
	t, ok := e.(TextBlock)
	return t, ok
}

func isTableBlock(e GeometricElement) (TableBlock, bool) {
	t, ok := e.(TableBlock)
	return t, ok
}

// lineGroup is the accumulator used by groupFragmentsIntoLines.
type lineGroup struct {
	topMean, yMin, yMax float64
	n                   int
	items               []page.TextCell
}

func (g *lineGroup) update(top, yMin, yMax float64) {
	g.topMean = (g.topMean*float64(g.n) + top) / float64(g.n+1)
	if yMin < g.yMin {
		g.yMin = yMin
	}
	if yMax > g.yMax {
		g.yMax = yMax
	}
	g.n++
}

// styleToken records the style of a [start,end) rune range within a
// TextBlock's Text, keyed by rune offset (see pdf-port/01 buildLineBlock
// note on Java UTF-16 vs Go rune indexing).
type styleToken struct {
	start, end int
	fontFamily string
	fontWeight int
	fontSize   float64
}

type styleSignature struct {
	fontFamily string
	fontWeight int
	fontSize   float64
}

type headerFooterLine struct {
	pageNo    int
	bbox      geom.Box
	signature string
}

type headerFooterFilter struct {
	pageCount           int
	repeatedZonesByPage map[int][]geom.Box
	repeatedSignatures  map[string]struct{}
}

// boxLLX/boxLLY/boxURX/boxURY read a BOTTOMLEFT-origin box using the Java
// Rectangle field names used throughout the spec, to keep algorithm code
// readable against the original document.
func boxLLX(b geom.Box) float64 { return b.L }
func boxLLY(b geom.Box) float64 { return b.B }
func boxURX(b geom.Box) float64 { return b.R }
func boxURY(b geom.Box) float64 { return b.T }

func toBottomLeft(b geom.Box, pageHeight float64) geom.Box {
	return b.WithOrigin(geom.BottomLeft, pageHeight)
}

func unionRect(a, b geom.Box) geom.Box {
	return geom.Box{
		L:      math.Min(a.L, b.L),
		B:      math.Min(a.B, b.B),
		R:      math.Max(a.R, b.R),
		T:      math.Max(a.T, b.T),
		Origin: geom.BottomLeft,
	}
}

func rectangleContains(outer, inner geom.Box, eps float64) bool {
	return boxLLX(outer)-eps <= boxLLX(inner) &&
		boxLLY(outer)-eps <= boxLLY(inner) &&
		boxURX(outer)+eps >= boxURX(inner) &&
		boxURY(outer)+eps >= boxURY(inner)
}

func rectanglesAdjacentOrOverlapping(a, b geom.Box, eps float64) bool {
	return !(boxLLX(a)-eps > boxURX(b) || boxLLX(b)-eps > boxURX(a) ||
		boxLLY(a)-eps > boxURY(b) || boxLLY(b)-eps > boxURY(a))
}

func topDistanceFromPage(rect geom.Box, pageHeight float64) float64 {
	return pageHeight - boxURY(rect)
}

// overlapRatio returns intersection area / a's area (asymmetric, see
// pdf-port/01 §字符/几何小工具).
func overlapRatio(a, b geom.Box) float64 {
	interLLX := math.Max(boxLLX(a), boxLLX(b))
	interLLY := math.Max(boxLLY(a), boxLLY(b))
	interURX := math.Min(boxURX(a), boxURX(b))
	interURY := math.Min(boxURY(a), boxURY(b))
	if interURX <= interLLX || interURY <= interLLY {
		return 0.0
	}
	inter := (interURX - interLLX) * (interURY - interLLY)
	area := math.Max(1.0, (boxURX(a)-boxLLX(a))*(boxURY(a)-boxLLY(a)))
	return inter / area
}

func verticalOverlapRatio(r geom.Box, gMinY, gMaxY float64) float64 {
	inter := math.Min(boxURY(r), gMaxY) - math.Max(boxLLY(r), gMinY)
	if inter <= 0 {
		return 0.0
	}
	h := math.Max(1e-6, boxURY(r)-boxLLY(r))
	return inter / h
}
