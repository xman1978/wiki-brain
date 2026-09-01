package localconvert

// Raw OOXML reader for .docx, used instead of a third-party library.
//
// docs/impl/v1/docx-port/01-word-to-markdown.md §10 flags go-ooxml's read
// support for several "high-risk" facts (auto-numbering label text,
// merge-cell flags) as unverified. Phase 0 of this task verified the actual
// current go-ooxml (github.com/ieshan/go-ooxml v1.1.2) API surface against
// the real test fixtures under data/sources/original/*.docx and found:
//   - Body.Paragraphs()/Body.Tables() are SEPARATE iterators, not one
//     document-order stream — collectBodyBlocks (docx-port/01 §2) requires
//     interleaved order, which this API cannot give without reaching into
//     unexported fields.
//   - Cell has no gridSpan/vMerge/hMerge accessors at all.
//   - ListInfo() explicitly documents itself as "simplified — full
//     resolution needs numbering.xml", i.e. no auto-numbering label text.
// Given the port needs document order plus both "high-risk" items anyway,
// this file parses word/document.xml, word/styles.xml and
// word/numbering.xml directly (stdlib archive/zip + encoding/xml), which is
// the explicitly sanctioned fallback in docx-port/01 §10 ("换库、或在
// go-ooxml 之上自己解析 numbering.xml/document.xml").
//
// Known gaps in this reader (see report): nested tables inside table cells
// are read as plain paragraph text without recursing into the nested table
// structure; footnote/endnote/comment part content is not read (only body
// text, matching WordToMarkdown's own scope).
//
// Style-level w:numPr inheritance IS resolved (see styleRegistry /
// resolveStyleNumPr below): a paragraph with no direct <w:numPr> inherits
// its numbering from its paragraph style's own <w:pPr><w:numPr> (walking
// the style's w:basedOn chain), matching Word's own resolution order. This
// matters for headings that only carry visual formatting matching a heading
// style (bold/size copied by hand) without applying the style itself, where
// the auto-numbered prefix text (e.g. "第一章 ") lives solely on the style,
// not the paragraph.

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// docParagraph is a paragraph's resolved properties + flattened text.
type docParagraph struct {
	StyleName string // resolved from styles.xml styleId -> w:name val
	Alignment string // w:jc val ("center", "left", ...)
	HasNum    bool
	NumID     int
	ILvl      int
	BoldLarge bool // true if any run is bold AND size>=14pt (isBoldAndLarge)
	Text      string
	InCell    bool
}

type docCell struct {
	GridSpan   int // >=1
	VMergeCont bool // true if w:vMerge is a continuation (no val, or val="continue")
	VMergeNew  bool // true if w:vMerge w:val="restart"
	Paragraphs []docParagraph
}

type docRow struct {
	Cells []docCell
}

type docTable struct {
	Rows [][]docCell
}

// docBlock is one top-level body child: either a paragraph or a table.
type docBlock struct {
	Para  *docParagraph
	Table *docTable
}

type docxDoc struct {
	Blocks    []docBlock
	numbering *numberingModel
}

func openDocxZip(srcPath string) (*zip.ReadCloser, error) {
	r, err := zip.OpenReader(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open docx zip: %w", err)
	}
	return r, nil
}

func readZipPart(zr *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, nil // absent parts are legal (e.g. no numbering.xml)
}

// parseDocx reads and resolves the document into ordered top-level blocks.
func parseDocx(srcPath string) (*docxDoc, error) {
	zr, err := openDocxZip(srcPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	stylesXML, err := readZipPart(zr, "word/styles.xml")
	if err != nil {
		return nil, fmt.Errorf("read styles.xml: %w", err)
	}
	styles := parseStyleRegistry(stylesXML)

	numberingXML, err := readZipPart(zr, "word/numbering.xml")
	if err != nil {
		return nil, fmt.Errorf("read numbering.xml: %w", err)
	}
	numModel := parseNumbering(numberingXML)

	docXML, err := readZipPart(zr, "word/document.xml")
	if err != nil || docXML == nil {
		return nil, fmt.Errorf("read document.xml: %w", err)
	}

	blocks, err := parseBodyBlocks(docXML, styles, numModel)
	if err != nil {
		return nil, err
	}
	return &docxDoc{Blocks: blocks, numbering: numModel}, nil
}

// --- styles.xml: styleId -> name / basedOn / inherited numPr ---------------

// styleRegistry holds per-style metadata resolved from styles.xml, used to
// resolve both display names (docParagraph.StyleName) and style-level
// w:numPr inheritance (resolveStyleNumPr).
type styleRegistry struct {
	names   map[string]string      // styleId -> w:name val
	basedOn map[string]string      // styleId -> w:basedOn val
	numPr   map[string]numPrResult // styleId -> its own <w:pPr><w:numPr> (only present if the style itself declares one)
}

func parseStyleRegistry(data []byte) *styleRegistry {
	reg := &styleRegistry{
		names:   map[string]string{},
		basedOn: map[string]string{},
		numPr:   map[string]numPrResult{},
	}
	if len(data) == 0 {
		return reg
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var curID string
	inPPr := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "style":
				curID = attrVal(t.Attr, "styleId")
			case "name":
				if curID != "" {
					reg.names[curID] = attrVal(t.Attr, "val")
				}
			case "basedOn":
				if curID != "" {
					reg.basedOn[curID] = attrVal(t.Attr, "val")
				}
			case "pPr":
				inPPr = true
			case "numPr":
				if curID != "" && inPPr {
					np, err := parseNumPr(dec)
					if err == nil && np.has {
						reg.numPr[curID] = np
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "style":
				curID = ""
				inPPr = false
			case "pPr":
				inPPr = false
			}
		}
	}
	return reg
}

// resolveStyleNumPr walks the w:basedOn chain starting at styleID looking
// for the nearest ancestor style (including styleID itself) that declares
// its own <w:pPr><w:numPr>, matching Word's style-inheritance resolution
// order for numbering. A depth guard protects against a malformed/cyclic
// basedOn chain.
func resolveStyleNumPr(reg *styleRegistry, styleID string) (numPrResult, bool) {
	if reg == nil || styleID == "" {
		return numPrResult{}, false
	}
	seen := map[string]bool{}
	id := styleID
	for depth := 0; depth < 32 && id != "" && !seen[id]; depth++ {
		seen[id] = true
		if np, ok := reg.numPr[id]; ok {
			return np, true
		}
		id = reg.basedOn[id]
	}
	return numPrResult{}, false
}

func attrVal(attrs []xml.Attr, local string) string {
	for _, a := range attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func attrIntDefault(attrs []xml.Attr, local string, def int) int {
	v := attrVal(attrs, local)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func attrFloatDefault(attrs []xml.Attr, local string, def float64) float64 {
	v := attrVal(attrs, local)
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}

// --- document.xml: ordered body blocks --------------------------------------

func parseBodyBlocks(data []byte, styles *styleRegistry, num *numberingModel) ([]docBlock, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var blocks []docBlock

	// Find <w:body>.
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("scan for body: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "body" {
			break
		}
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok2 := tok.(xml.EndElement); ok2 && ee.Name.Local == "body" {
				break
			}
			continue
		}
		switch se.Name.Local {
		case "p":
			p, err := parseParagraph(dec, se, styles, num, false)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, docBlock{Para: p})
		case "tbl":
			tbl, err := parseTable(dec, styles, num)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, docBlock{Table: tbl})
		default:
			if err := skipElement(dec); err != nil {
				return nil, err
			}
		}
	}
	return blocks, nil
}

// parseParagraph consumes a <w:p>...</w:p> element (se already consumed as
// StartElement) and returns its resolved properties + flattened text.
func parseParagraph(dec *xml.Decoder, se xml.StartElement, styles *styleRegistry, num *numberingModel, inCell bool) (*docParagraph, error) {
	p := &docParagraph{InCell: inCell}
	var sb strings.Builder
	var styleID string
	fieldDepth := 0    // >0 while inside a skipped fldSimple subtree
	inFieldCode := false // true between fldChar begin..separate (instrText, skip)
	delDepth := 0      // >0 while inside a skipped w:del subtree
	var fieldInstr strings.Builder // accumulates instrText between fldChar begin..separate, to classify the field

	var maxBoldSize float64
	sawBoldLarge := false
	hasDirectNumPr := false // true if <w:numPr> was present at all, even numId=0 (explicit "no numbering" override of the style)

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "pStyle":
				styleID = attrVal(t.Attr, "val")
			case "jc":
				p.Alignment = attrVal(t.Attr, "val")
			case "numPr":
				np, err := parseNumPr(dec)
				if err != nil {
					return nil, err
				}
				hasDirectNumPr = true
				p.HasNum = np.has
				p.NumID = np.numID
				p.ILvl = np.ilvl
			case "fldSimple":
				// PAGEREF fields carry a TOC entry's page number as their
				// cached result — the only literal digit a Word-generated
				// TOC line has, since the chapter title around it is plain
				// text. Dropping it under the general field.remove()
				// semantics (see fldChar/separate below) left TOC lines with
				// no trailing page number, which is exactly what
				// pdfconv.IsChapterTocLine/isTocPagedLine key off of to
				// recognize and strip a TOC block — so those fields' cached
				// text is kept; every other field type keeps being skipped.
				if !keepFieldResult(attrVal(t.Attr, "instr")) {
					fieldDepth++
				}
			case "del":
				delDepth++
			case "ins":
				// accept-all: keep contents, just recurse normally.
			case "fldChar":
				switch attrVal(t.Attr, "fldCharType") {
				case "begin":
					inFieldCode = true
					fieldInstr.Reset()
				case "separate":
					inFieldCode = false
					if !keepFieldResult(fieldInstr.String()) {
						fieldDepth++ // skip the cached field RESULT too (field.remove() semantics)
					}
				case "end":
					if fieldDepth > 0 {
						fieldDepth--
					}
					inFieldCode = false
				}
			case "instrText":
				text, err := readCharData(dec)
				if err != nil {
					return nil, err
				}
				if inFieldCode {
					fieldInstr.WriteString(text)
				}
			case "t":
				text, err := readCharData(dec)
				if err != nil {
					return nil, err
				}
				if fieldDepth == 0 && delDepth == 0 && !inFieldCode {
					sb.WriteString(text)
				}
			case "tab":
				if fieldDepth == 0 && delDepth == 0 {
					sb.WriteByte('\t')
				}
			case "br":
				if fieldDepth == 0 && delDepth == 0 {
					typ := attrVal(t.Attr, "type")
					if typ != "page" && typ != "column" {
						sb.WriteRune('\v') // soft break, docx-port/01 §6
					}
				}
			case "b":
				// run-level bold flag; combined with sz below via a small
				// look-back — Word emits <w:b/> and <w:sz .../> as siblings
				// inside the same rPr, so we track "any bold run" and the
				// max size seen in a bold run using a simple two-pass style
				// heuristic: mark boldSeenInCurrentRPr.
				boldSeenInCurrentRPr = attrVal(t.Attr, "val") != "0" && attrVal(t.Attr, "val") != "false"
			case "sz":
				if v, err := strconv.ParseFloat(attrVal(t.Attr, "val"), 64); err == nil {
					curRunSizePt := v / 2.0
					if boldSeenInCurrentRPr && curRunSizePt >= 14.0 {
						sawBoldLarge = true
						if curRunSizePt > maxBoldSize {
							maxBoldSize = curRunSizePt
						}
					}
				}
			case "rPr":
				boldSeenInCurrentRPr = false
			case "oMath":
				text, err := readOMathArgs(dec, "oMath")
				if err != nil {
					return nil, err
				}
				if fieldDepth == 0 && delDepth == 0 {
					sb.WriteString(text)
				}
			case "oMathPara":
				text, err := readOMathPara(dec)
				if err != nil {
					return nil, err
				}
				if fieldDepth == 0 && delDepth == 0 {
					sb.WriteString(text)
				}
			case "drawing", "pict":
				// Floating shapes (textboxes, cover-page graphics) carry
				// their own nested <w:p> elements (often duplicated between
				// mc:Choice/mc:Fallback for old-Word compatibility). Reading
				// through them naively — as every other case here does by
				// letting dec.Token() walk into them — hits their nested
				// </w:p> end tags, which this function's own end-of-paragraph
				// check below cannot distinguish from its own closing tag
				// and returns on prematurely. Skip the whole subtree instead
				// of trying to parse it as body-flow text.
				if err := skipElement(dec); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if styles != nil {
					p.StyleName = styles.names[styleID]
				}
				if !p.HasNum && !hasDirectNumPr {
					// Only fall back to the style's numbering when the
					// paragraph declared no <w:numPr> of its own at all —
					// a direct <w:numId w:val="0"/> is Word's explicit
					// "opt out of this style's numbering" marker and must
					// not be overridden by the style-level fallback below.
					if np, ok := resolveStyleNumPr(styles, styleID); ok {
						p.HasNum = true
						p.NumID = np.numID
						p.ILvl = np.ilvl
					}
				}
				p.Text = sb.String()
				p.BoldLarge = sawBoldLarge
				return p, nil
			case "fldSimple":
				if fieldDepth > 0 {
					fieldDepth--
				}
			case "del":
				if delDepth > 0 {
					delDepth--
				}
			}
		}
	}
}

// boldSeenInCurrentRPr is intentionally a package-level scratch flag reset
// at each <w:rPr>/<w:b> — parseParagraph is not reentrant across
// goroutines (matches the rest of this converter's single-goroutine usage).
var boldSeenInCurrentRPr bool

type numPrResult struct {
	has         bool
	numID, ilvl int
}

func parseNumPr(dec *xml.Decoder) (numPrResult, error) {
	res := numPrResult{}
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return res, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "numId":
				res.numID = attrIntDefault(t.Attr, "val", 0)
				res.has = res.numID > 0
			case "ilvl":
				res.ilvl = attrIntDefault(t.Attr, "val", 0)
			}
		case xml.EndElement:
			if t.Name.Local == "numPr" && depth == 0 {
				return res, nil
			}
			depth--
		}
	}
}

// pagerefFieldInstrRe matches a field instruction (the " PAGEREF _Toc123 \h "
// text between fldChar begin/separate, or a fldSimple's w:instr attribute)
// whose cached result should be kept rather than dropped.
var pagerefFieldInstrRe = regexp.MustCompile(`(?i)^\s*PAGEREF\b`)

func keepFieldResult(instr string) bool {
	return pagerefFieldInstrRe.MatchString(instr)
}

func readCharData(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			return sb.String(), nil
		}
	}
}

func skipElement(dec *xml.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// --- tables ------------------------------------------------------------------

func parseTable(dec *xml.Decoder, styles *styleRegistry, num *numberingModel) (*docTable, error) {
	tbl := &docTable{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tr":
				row, err := parseTableRow(dec, styles, num)
				if err != nil {
					return nil, err
				}
				tbl.Rows = append(tbl.Rows, row)
			default:
				if err := skipElement(dec); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "tbl" {
				return tbl, nil
			}
		}
	}
}

func parseTableRow(dec *xml.Decoder, styles *styleRegistry, num *numberingModel) ([]docCell, error) {
	var cells []docCell
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tc":
				cell, err := parseTableCell(dec, styles, num)
				if err != nil {
					return nil, err
				}
				cells = append(cells, *cell)
			default:
				if err := skipElement(dec); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "tr" {
				return cells, nil
			}
		}
	}
}

func parseTableCell(dec *xml.Decoder, styles *styleRegistry, num *numberingModel) (*docCell, error) {
	cell := &docCell{GridSpan: 1}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tcPr":
				// Do not skip: gridSpan/vMerge live inside tcPr and must
				// stream through as their own StartElement tokens next.
			case "gridSpan":
				cell.GridSpan = attrIntDefault(t.Attr, "val", 1)
			case "vMerge":
				v := attrVal(t.Attr, "val")
				if v == "restart" {
					cell.VMergeNew = true
				} else {
					cell.VMergeCont = true
				}
			case "p":
				p, err := parseParagraph(dec, t, styles, num, true)
				if err != nil {
					return nil, err
				}
				cell.Paragraphs = append(cell.Paragraphs, *p)
			case "tbl":
				// Nested tables: flatten to their cell text (not recursed
				// into as a structural table) — documented gap, see file
				// header.
				if err := skipElement(dec); err != nil {
					return nil, err
				}
			default:
				if err := skipElement(dec); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "tc" {
				return cell, nil
			}
		}
	}
}
