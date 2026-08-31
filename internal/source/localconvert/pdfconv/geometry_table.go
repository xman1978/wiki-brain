package pdfconv

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/ivanvanderbyl/docmill/v2/pkg/geom"
	"github.com/ivanvanderbyl/docmill/v2/pkg/page"
	docpdf "github.com/ivanvanderbyl/docmill/v2/pkg/pdf"
)

// absorbedCell is our substitute for Aspose's AbsorbedCell: a grid cell
// already located and text-filled by ruling-line reconstruction (see
// buildAbsorbedTablesFromRulings below). rowSpan/colSpan already reflect any
// merged cells detected from missing interior ruling lines.
type absorbedCell struct {
	row, col, rowSpan, colSpan int
	rect                       geom.Box
	text                       string
}

// absorbedTable is our substitute for Aspose's AbsorbedTable: a rectangular
// grid of absorbedCell built from one connected ruling-line region. This is
// the documented architecture gap (pdf-port/01 §extractTableBlocks "Aspose
// → docmill gap", 方案 B) — docmill has no table/border detector, so instead
// of Aspose's AbsorbedTable → AbsorbedRow → AbsorbedCell coming for free,
// we reconstruct an equivalent structure ourselves from RulingSegments
// before handing it to the (faithfully ported) clustering/merge algorithms
// below, which are otherwise unmodified from the Java source.
type absorbedTable struct {
	rect  geom.Box
	rows  int
	cols  int
	cells []absorbedCell // len == rows*cols conceptually, one entry per top-left grid origin
}

func (t absorbedTable) cellAt(r, c int) *absorbedCell {
	for i := range t.cells {
		if t.cells[i].row == r && t.cells[i].col == c {
			return &t.cells[i]
		}
	}
	return nil
}

const rulingLineTolerancePt = 2.0

// minTableGridSliverGapPt is the smallest gap between two adjacent grid
// boundaries that can plausibly hold a real content row/column. Word/WPS
// often draws a table's outer border as two parallel ruling lines a couple
// of points apart (a thick outer frame plus a thin inner border) rather
// than a single line — RulingSegments() reports both as separate rulings,
// so clusterBoundaries's tight tableGeometryEpsilonPt (1.5pt, tuned for
// real adjacent-column detection) doesn't merge them and a spurious
// ~2.5pt-tall/wide "row 0" or "col 0" sliver appears at the frame's outer
// edge. Because that sliver has no text ruling reaching into it
// (interior row/column dividers stop short of the outer frame), the
// merged-cell union-find below finds no boundary within it and merges
// every row (or column) inside the sliver into one strip — and since
// mcell only records a bounding rectangle, that strip's bbox is the
// *entire* table, so the reconstructed grid ends up with a duplicate
// "everything in one cell" block layered on top of the real, correctly
// gridded one. A real text row/column is never this thin (even a single
// CJK glyph is >6pt), so collapsing any outer gap under this threshold
// into its neighbor is safe and simply reproduces what the double ruling
// looks like to a reader: one border line, not a hidden extra row.
const minTableGridSliverGapPt = 4.0

// collapseOuterDoubleBorderGaps drops a sliver-thin outer boundary gap (see
// minTableGridSliverGapPt) at either end of an ascending boundary list,
// merging it into the adjacent real row/column. The *outer* of the two
// close boundaries is the one dropped, not the inner one: the interior
// row/column divider rulings are themselves inset from the outer frame by
// roughly the same sliver-sized gap (that's the same double-border
// draw — see minTableGridSliverGapPt), so they reach the inner boundary
// but not the outer one. Keeping the inner boundary as the new edge means
// hasHLineAt/hasVLineAt's reach checks against it still succeed; keeping
// the outer one would just move the "divider doesn't reach the edge"
// failure from a 2-line sliver problem to a 1-line "first/last row or
// column merges into everything" problem instead of actually fixing it.
func collapseOuterDoubleBorderGaps(bounds []float64, minGapPt float64) []float64 {
	if len(bounds) < 3 {
		return bounds
	}
	out := append([]float64(nil), bounds...)
	if out[1]-out[0] < minGapPt {
		out = out[1:]
	}
	if len(out) >= 3 && out[len(out)-1]-out[len(out)-2] < minGapPt {
		out = out[:len(out)-1]
	}
	return out
}

// buildAbsorbedTablesFromRulings reconstructs candidate tables from a page's
// stroked ruling segments: cluster segments into connected geometric
// regions, derive row/column grid lines from the horizontal/vertical
// segments in each region, detect merged cells from missing interior
// boundary segments, then assign text cells to grid positions by center
// point. Regions that fail to yield a >=1x1 grid are dropped.
func buildAbsorbedTablesFromRulings(rulings []page.RulingSegment, cells []page.TextCell, pageHeight float64, cfg Config) []absorbedTable {
	type seg struct {
		horizontal bool
		fromX, toX float64
		y          float64 // horizontal: shared y; vertical: unused
		fromY, toY float64 // vertical: shared x below; for horizontal these track x-span info unused
		x          float64 // vertical: shared x
		box        geom.Box
	}
	var segs []seg
	for _, r := range rulings {
		b := geom.Box{L: math.Min(r.FromX, r.ToX), R: math.Max(r.FromX, r.ToX),
			T: math.Max(pageHeight-r.FromY, pageHeight-r.ToY), B: math.Min(pageHeight-r.FromY, pageHeight-r.ToY),
			Origin: geom.BottomLeft}
		// r is in TOPLEFT coords (per docmill RulingSegments doc); convert to BOTTOMLEFT.
		fy := pageHeight - r.FromY
		ty := pageHeight - r.ToY
		dx := math.Abs(r.ToX - r.FromX)
		dy := math.Abs(ty - fy)
		if dx < 1.0 && dy < 1.0 {
			continue // degenerate point, not a line
		}
		if dy <= 1.0 && dx >= 3.0 {
			segs = append(segs, seg{horizontal: true, fromX: math.Min(r.FromX, r.ToX), toX: math.Max(r.FromX, r.ToX), y: (fy + ty) / 2, box: b})
		} else if dx <= 1.0 && dy >= 3.0 {
			segs = append(segs, seg{horizontal: false, fromY: math.Min(fy, ty), toY: math.Max(fy, ty), x: (r.FromX + r.ToX) / 2, box: b})
		}
	}
	if len(segs) == 0 {
		return nil
	}

	// Cluster segments into connected regions (union-find on adjacent/overlapping boxes).
	parent := make([]int, len(segs))
	for i := range parent {
		parent[i] = i
	}
	var findRoot func(int) int
	findRoot = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := findRoot(a), findRoot(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if rectanglesAdjacentOrOverlapping(segs[i].box, segs[j].box, rulingLineTolerancePt*2) {
				union(i, j)
			}
		}
	}
	groups := map[int][]int{}
	for i := range segs {
		r := findRoot(i)
		groups[r] = append(groups[r], i)
	}

	var tables []absorbedTable
	for _, idxs := range groups {
		var hLines, vLines []seg
		var regionUnion geom.Box
		first := true
		for _, i := range idxs {
			s := segs[i]
			if first {
				regionUnion = s.box
				first = false
			} else {
				regionUnion = unionRect(regionUnion, s.box)
			}
			if s.horizontal {
				hLines = append(hLines, s)
			} else {
				vLines = append(vLines, s)
			}
		}
		if len(hLines) < 2 || len(vLines) < 2 {
			continue // need at least 2 horizontal + 2 vertical lines to bound >=1 cell
		}
		var ys, xs []float64
		for _, s := range hLines {
			ys = append(ys, s.y)
		}
		for _, s := range vLines {
			xs = append(xs, s.x)
		}
		yBounds := collapseOuterDoubleBorderGaps(clusterBoundaries(ys, tableGeometryEpsilonPt), minTableGridSliverGapPt)
		xBounds := collapseOuterDoubleBorderGaps(clusterBoundaries(xs, tableGeometryEpsilonPt), minTableGridSliverGapPt)
		rowCount := len(yBounds) - 1
		colCount := len(xBounds) - 1
		if rowCount < 1 || colCount < 1 {
			continue
		}

		// hasHLineAt(y, x0, x1): is there a horizontal ruling covering [x0,x1] at height y?
		hasHLineAt := func(y, x0, x1 float64) bool {
			for _, s := range hLines {
				if math.Abs(s.y-y) <= rulingLineTolerancePt && s.fromX <= x0+rulingLineTolerancePt && s.toX >= x1-rulingLineTolerancePt {
					return true
				}
			}
			return false
		}
		hasVLineAt := func(x, y0, y1 float64) bool {
			for _, s := range vLines {
				if math.Abs(s.x-x) <= rulingLineTolerancePt && s.fromY <= y0+rulingLineTolerancePt && s.toY >= y1-rulingLineTolerancePt {
					return true
				}
			}
			return false
		}

		// Union-find over grid cells (row,col) -> merge across a missing
		// interior boundary, to discover merged cells.
		gcount := rowCount * colCount
		gp := make([]int, gcount)
		for i := range gp {
			gp[i] = i
		}
		var gfind func(int) int
		gfind = func(i int) int {
			for gp[i] != i {
				gp[i] = gp[gp[i]]
				i = gp[i]
			}
			return i
		}
		gunion := func(a, b int) {
			ra, rb := gfind(a), gfind(b)
			if ra != rb {
				gp[ra] = rb
			}
		}
		idx := func(r, c int) int { return r*colCount + c }
		for r := 0; r < rowCount; r++ {
			y0, y1 := yBounds[r], yBounds[r+1]
			for c := 0; c < colCount-1; c++ {
				x := xBounds[c+1]
				if !hasVLineAt(x, y0, y1) {
					gunion(idx(r, c), idx(r, c+1))
				}
			}
		}
		for c := 0; c < colCount; c++ {
			x0, x1 := xBounds[c], xBounds[c+1]
			for r := 0; r < rowCount-1; r++ {
				y := yBounds[r+1]
				if !hasHLineAt(y, x0, x1) {
					gunion(idx(r, c), idx(r+1, c))
				}
			}
		}

		// Collect merged-cell rectangles per root.
		type mcell struct{ minR, maxR, minC, maxC int }
		mcells := map[int]*mcell{}
		for r := 0; r < rowCount; r++ {
			for c := 0; c < colCount; c++ {
				root := gfind(idx(r, c))
				mc, ok := mcells[root]
				if !ok {
					mcells[root] = &mcell{minR: r, maxR: r, minC: c, maxC: c}
					continue
				}
				if r < mc.minR {
					mc.minR = r
				}
				if r > mc.maxR {
					mc.maxR = r
				}
				if c < mc.minC {
					mc.minC = c
				}
				if c > mc.maxC {
					mc.maxC = c
				}
			}
		}

		var absCells []absorbedCell
		for _, mc := range mcells {
			rect := geom.Box{
				L: xBounds[mc.minC], R: xBounds[mc.maxC+1],
				B: yBounds[mc.minR], T: yBounds[mc.maxR+1],
				Origin: geom.BottomLeft,
			}
			// row is measured top-down: row 0 = topmost. yBounds is ascending
			// (bottom-up), so the topmost row uses the highest y indices.
			topRow := rowCount - 1 - mc.maxR
			text := textInRectFromCells(cells, rect, pageHeight, cfg)
			absCells = append(absCells, absorbedCell{
				row: topRow, col: mc.minC,
				rowSpan: mc.maxR - mc.minR + 1, colSpan: mc.maxC - mc.minC + 1,
				rect: rect, text: text,
			})
		}
		tables = append(tables, absorbedTable{rect: regionUnion, rows: rowCount, cols: colCount, cells: absCells})
	}
	return tables
}

// textInRectFromCells assigns text cells whose center falls in rect,
// reading order top-to-bottom/left-to-right (mirrors extractCellText).
//
// pdf-port/01's extractCellText spec sorts fragments by raw -URY/LLX with
// no real line grouping ("这里没有真正按行分组，只是简单排序") — a strict,
// tolerance-free per-fragment Y comparison that's safe against Aspose's
// fairly uniform per-run box heights, but not against docmill's tight
// per-glyph ink boxes: even after the box-height normalization in
// toBottomLeftCells (geometry_headerfooter.go), a single-line, mixed-script
// cell like "V1.0" (Latin/digit glyphs sit shorter than the CJK ratio that
// normalization calibrates against) can still have per-glyph Y noise on
// the order of a point or two, which a strict sort has zero tolerance for
// and can silently reorder ("V1.0" -> ".0V1"). groupFragmentsIntoLines
// (already used two lines down by extractCellLines for the multi-line
// case) tolerates exactly this kind of noise via its yTol window, so it's
// used here too — for the common single-line cell this still collapses to
// one group and one sort-by-LLX pass, identical in effect to the spec's
// simple sort; it only changes behavior when raw Y noise would otherwise
// have split one visual line into a false ordering.
func textInRectFromCells(cells []page.TextCell, rect geom.Box, pageHeight float64, cfg Config) string {
	var matched []page.TextCell
	for _, c := range cells {
		cx := (boxLLX(c.Box) + boxURX(c.Box)) / 2
		cy := (boxLLY(c.Box) + boxURY(c.Box)) / 2
		if cx >= rect.L && cx <= rect.R && cy >= rect.B && cy <= rect.T {
			matched = append(matched, c)
		}
	}
	lines := groupFragmentsIntoLines(matched, pageHeight, cfg)
	var parts []string
	for _, line := range lines {
		sorted := make([]page.TextCell, len(line))
		copy(sorted, line)
		sort.SliceStable(sorted, func(i, j int) bool { return boxLLX(sorted[i].Box) < boxLLX(sorted[j].Box) })
		if t := joinFragmentsReadingOrder(sorted); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// joinFragmentsReadingOrder concatenates cells in the order given — callers
// are responsible for ordering (both current callers already group by
// visual line via groupFragmentsIntoLines and sort each line by LLX; see
// textInRectFromCells's doc comment for why that replaced a raw per-glyph
// Y sort here).
func joinFragmentsReadingOrder(cells []page.TextCell) string {
	var b strings.Builder
	var prevText string
	var prevBox *geom.Box
	for _, c := range cells {
		box := c.Box
		if b.Len() > 0 && shouldInsertSpaceByGeometry(prevText, c.Text, prevBox, &box) {
			b.WriteByte(' ')
		}
		b.WriteString(c.Text)
		prevText = c.Text
		bc := box
		prevBox = &bc
	}
	return b.String()
}

// extractCellLines ports pdf-port/01 §extractCellLines: same fragments as
// extractCellText but grouped into visual lines to preserve line breaks.
func extractCellLines(cells []page.TextCell, rect geom.Box, pageHeight float64, cfg Config) []string {
	var matched []page.TextCell
	for _, c := range cells {
		cx := (boxLLX(c.Box) + boxURX(c.Box)) / 2
		cy := (boxLLY(c.Box) + boxURY(c.Box)) / 2
		if cx >= rect.L && cx <= rect.R && cy >= rect.B && cy <= rect.T {
			matched = append(matched, c)
		}
	}
	lines := groupFragmentsIntoLines(matched, pageHeight, cfg)
	var out []string
	for _, line := range lines {
		sorted := make([]page.TextCell, len(line))
		copy(sorted, line)
		sort.SliceStable(sorted, func(i, j int) bool { return boxLLX(sorted[i].Box) < boxLLX(sorted[j].Box) })
		text := normalizeText(joinFragmentsReadingOrder(sorted), cfg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// hasRaggedRowCellCounts ports pdf-port/01's namesake.
func hasRaggedRowCellCounts(t absorbedTable) bool {
	countByRow := map[int]int{}
	for _, c := range t.cells {
		countByRow[c.row]++
	}
	maxCells := 0
	for _, n := range countByRow {
		if n > maxCells {
			maxCells = n
		}
	}
	for _, n := range countByRow {
		if n != maxCells {
			return true
		}
	}
	return false
}

func isSingleCellTable(t absorbedTable) bool {
	return t.rows == 1 && t.cols == 1
}

func isSingleCellTableCandidateAbs(t absorbedTable) bool {
	return isSingleCellTable(t)
}

// clusterAbsorbedTables ports pdf-port/01's namesake (union-find geometric clustering).
func clusterAbsorbedTables(tables []absorbedTable) [][]int {
	n := len(tables)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var findRoot func(int) int
	findRoot = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	unionSets := func(a, b int) {
		ra, rb := findRoot(a), findRoot(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if rectanglesAdjacentOrOverlapping(tables[i].rect, tables[j].rect, tableGeometryEpsilonPt) {
				unionSets(i, j)
			}
		}
	}
	order := map[int]int{}
	var groups [][]int
	for i := 0; i < n; i++ {
		root := findRoot(i)
		gi, ok := order[root]
		if !ok {
			gi = len(groups)
			order[root] = gi
			groups = append(groups, nil)
		}
		groups[gi] = append(groups[gi], i)
	}
	return groups
}

// dropContainerDuplicates ports pdf-port/01's namesake.
func dropContainerDuplicates(cluster []absorbedTable) []absorbedTable {
	if len(cluster) <= 1 {
		return cluster
	}
	var effective []absorbedTable
	for i, candidate := range cluster {
		if !isSingleCellTableCandidateAbs(candidate) {
			effective = append(effective, candidate)
			continue
		}
		var others *geom.Box
		for j, o := range cluster {
			if j == i {
				continue
			}
			if others == nil {
				b := o.rect
				others = &b
			} else {
				u := unionRect(*others, o.rect)
				others = &u
			}
		}
		if others != nil && rectangleContains(candidate.rect, *others, tableGeometryEpsilonPt) {
			continue // shell duplicate
		}
		effective = append(effective, candidate)
	}
	if len(effective) == 0 {
		return cluster
	}
	return effective
}

func shellRegionOf(cluster, effective []absorbedTable) *geom.Box {
	effSet := map[geom.Box]bool{}
	for _, e := range effective {
		effSet[e.rect] = true
	}
	var region *geom.Box
	for _, t := range cluster {
		if effSet[t.rect] {
			continue
		}
		if region == nil {
			b := t.rect
			region = &b
		} else {
			u := unionRect(*region, t.rect)
			region = &u
		}
	}
	return region
}

func buildSimpleTableBlock(t absorbedTable, pageNo int, pageHeight float64, cfg Config, index int) *TableBlock {
	rowCount := t.rows
	colCount := t.cols
	var cellData []TableCellData
	for _, c := range t.cells {
		cellData = append(cellData, TableCellData{Row: c.row, Col: c.col, RowSpan: 1, ColSpan: 1, Text: normalizeTableCellText(c.text, cfg)})
	}
	if len(cellData) == 0 {
		return nil
	}
	bbox := t.rect
	tb := &TableBlock{
		baseElement: baseElement{
			ID:          blockID("TableBlock", pageNo, index),
			PageNo:      pageNo,
			Bbox:        &bbox,
			TopDistance: topDistanceFromPage(bbox, pageHeight),
			Left:        boxLLX(bbox),
		},
		RowCount: rowCount,
		ColCount: max(colCount, 1),
		Cells:    cellData,
	}
	if isSingleCellTable(t) && len(t.cells) == 1 {
		// caller fills SingleCellLines with real fragment-grouped lines when available.
	}
	return tb
}

func buildMergedTableBlock(effective []absorbedTable, shellRegion *geom.Box, pageNo int, pageHeight float64, cfg Config, index int) *TableBlock {
	var cellRects []geom.Box
	var cellTexts []string
	var union *geom.Box
	for _, t := range effective {
		for _, c := range t.cells {
			cellRects = append(cellRects, c.rect)
			cellTexts = append(cellTexts, normalizeTableCellText(c.text, cfg))
			if union == nil {
				b := c.rect
				union = &b
			} else {
				u := unionRect(*union, c.rect)
				union = &u
			}
		}
	}
	if len(cellRects) == 0 || union == nil {
		return nil
	}
	if shellRegion != nil {
		u := unionRect(*union, *shellRegion)
		union = &u
	}

	var xs, ys []float64
	for _, r := range cellRects {
		xs = append(xs, boxLLX(r), boxURX(r))
		ys = append(ys, boxLLY(r), boxURY(r))
	}
	if shellRegion != nil {
		xs = append(xs, boxLLX(*shellRegion), boxURX(*shellRegion))
		ys = append(ys, boxLLY(*shellRegion), boxURY(*shellRegion))
	}
	xBounds := clusterBoundaries(xs, tableGeometryEpsilonPt)
	yBounds := clusterBoundaries(ys, tableGeometryEpsilonPt)
	colCount := len(xBounds) - 1
	rowCount := len(yBounds) - 1
	if colCount < 1 || rowCount < 1 {
		return nil
	}

	covered := make([][]bool, rowCount)
	for i := range covered {
		covered[i] = make([]bool, colCount)
	}
	colHasKeptCell := make([]bool, colCount)
	type gridKey struct{ row, col int }
	byGridOrigin := map[gridKey]TableCellData{}

	for i, rect := range cellRects {
		leftIdx := nearestBoundaryIndex(xBounds, boxLLX(rect))
		rightIdx := nearestBoundaryIndex(xBounds, boxURX(rect))
		bottomIdx := nearestBoundaryIndex(yBounds, boxLLY(rect))
		topIdx := nearestBoundaryIndex(yBounds, boxURY(rect))
		col := leftIdx
		row := rowCount - topIdx
		if row < 0 || row >= rowCount || col < 0 || col >= colCount {
			continue
		}
		colSpan := clampInt(max(1, rightIdx-leftIdx), 1, colCount-col)
		rowSpan := clampInt(max(1, topIdx-bottomIdx), 1, rowCount-row)
		key := gridKey{row, col}
		existing, ok := byGridOrigin[key]
		if !ok || (existing.Text == "" && cellTexts[i] != "") {
			byGridOrigin[key] = TableCellData{Row: row, Col: col, RowSpan: rowSpan, ColSpan: colSpan, Text: cellTexts[i]}
		}
		for rr := row; rr < row+rowSpan && rr < rowCount; rr++ {
			for cc := col; cc < col+colSpan && cc < colCount; cc++ {
				covered[rr][cc] = true
			}
		}
		for cc := col; cc < col+colSpan && cc < colCount; cc++ {
			colHasKeptCell[cc] = true
		}
	}
	if len(byGridOrigin) == 0 {
		return nil
	}

	var cells []TableCellData
	for _, v := range byGridOrigin {
		cells = append(cells, v)
	}
	tb := &TableBlock{
		baseElement: baseElement{
			ID:          blockID("TableBlock", pageNo, index),
			PageNo:      pageNo,
			Bbox:        union,
			TopDistance: topDistanceFromPage(*union, pageHeight),
			Left:        boxLLX(*union),
		},
		RowCount: rowCount,
		ColCount: colCount,
		Cells:    cells,
	}
	return tb
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func nearestBoundaryIndex(ascending []float64, value float64) int {
	best := 0
	bestDiff := math.Inf(1)
	for i, b := range ascending {
		d := math.Abs(b - value)
		if d < bestDiff {
			bestDiff = d
			best = i
		}
	}
	return best
}

func intervalIndex(ascendingBounds []float64, value float64) int {
	for i := len(ascendingBounds) - 2; i >= 1; i-- {
		if value >= ascendingBounds[i] {
			return i
		}
	}
	return 0
}

func clusterBoundaries(raw []float64, tolerance float64) []float64 {
	if len(raw) == 0 {
		return nil
	}
	sorted := append([]float64(nil), raw...)
	sort.Float64s(sorted)
	out := []float64{sorted[0]}
	for _, v := range sorted[1:] {
		if v-out[len(out)-1] > tolerance {
			out = append(out, v)
		}
	}
	return out
}

// extractTableBlocks ports pdf-port/01 §extractTableBlocks, substituting
// buildAbsorbedTablesFromRulings for Aspose's TableAbsorber (see
// absorbedTable doc comment).
// rulingSegmentsCapable mirrors docmill's unexported rulingSegmentProvider
// capability interface (pkg/pdf.Page itself only guarantees
// Size/TextCells/TextInRect); the concrete *parser.Page implements it, so we
// assert to our own copy of the method set structurally.
type rulingSegmentsCapable interface {
	RulingSegments(ctx context.Context) ([]page.RulingSegment, error)
}

func extractTableBlocks(ctx context.Context, pg docpdf.Page, pageNo int, pageHeight, pageWidth float64, cfg Config) ([]TableBlock, error) {
	var rulings []page.RulingSegment
	if rp, ok := pg.(rulingSegmentsCapable); ok {
		r, err := rp.RulingSegments(ctx)
		if err != nil {
			return nil, err
		}
		rulings = r
	}
	cells, err := pg.TextCells(ctx)
	if err != nil {
		return nil, err
	}
	cells = toBottomLeftCells(cells, pageHeight)

	absorbed := buildAbsorbedTablesFromRulings(rulings, cells, pageHeight, cfg)
	if len(absorbed) == 0 {
		return nil, nil
	}
	clusters := clusterAbsorbedTables(absorbed)

	var out []TableBlock
	index := 0
	for _, idxs := range clusters {
		var cluster []absorbedTable
		for _, i := range idxs {
			cluster = append(cluster, absorbed[i])
		}
		effective := dropContainerDuplicates(cluster)
		if len(effective) == 0 {
			continue
		}
		shellRegion := shellRegionOf(cluster, effective)
		simpleUnmerged := len(effective) == 1 && shellRegion == nil && !hasRaggedRowCellCounts(effective[0])

		var tb *TableBlock
		if simpleUnmerged {
			tb = buildSimpleTableBlock(effective[0], pageNo, pageHeight, cfg, index)
			if tb != nil && isSingleCellTable(effective[0]) {
				tb.SingleCellLines = extractCellLines(cells, effective[0].rect, pageHeight, cfg)
			}
		} else {
			tb = buildMergedTableBlock(effective, shellRegion, pageNo, pageHeight, cfg, index)
		}
		if tb != nil {
			out = append(out, *tb)
			index++
		}
	}
	return out, nil
}
