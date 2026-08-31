package pptx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const relTypeNotes = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide"

type presentationXML struct {
	SldIDLst []slideID `xml:"sldIdLst>sldId"`
}

type slideID struct {
	RelID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

type slideXML struct {
	Shapes   []shapeXML        `xml:"cSld>spTree>sp"`
	Pics     []picXML          `xml:"cSld>spTree>pic"`
	Graphics []graphicFrameXML `xml:"cSld>spTree>graphicFrame"`
}

type shapeXML struct {
	NV   nvSpPrXML  `xml:"nvSpPr"`
	Body *txBodyXML `xml:"txBody"`
}

type nvSpPrXML struct {
	CNvPr cNvPrXML `xml:"cNvPr"`
	NvPr  nvPrXML  `xml:"nvPr"`
}

type nvPrXML struct {
	PH *phXML `xml:"ph"`
}

type cNvPrXML struct {
	Name  string `xml:"name,attr"`
	Descr string `xml:"descr,attr"`
}

type phXML struct {
	Type string `xml:"type,attr"`
}

type txBodyXML struct {
	Paragraphs []paragraphXML `xml:"p"`
}

type paragraphXML struct {
	Runs []runXML `xml:"r"`
	Flds []runXML `xml:"fld"`
	PPr  *pPrXML  `xml:"pPr"`
}

type pPrXML struct {
	Level int `xml:"lvl,attr"`
}

type runXML struct {
	Text string `xml:"t"`
}

type picXML struct {
	NV nvPicPrXML `xml:"nvPicPr"`
}

type nvPicPrXML struct {
	CNvPr cNvPrXML `xml:"cNvPr"`
}

type graphicFrameXML struct {
	Table *tableXML `xml:"graphic>graphicData>tbl"`
}

type tableXML struct {
	Rows []tableRowXML `xml:"tr"`
}

type tableRowXML struct {
	Cells []tableCellXML `xml:"tc"`
}

type tableCellXML struct {
	Body txBodyXML `xml:"txBody"`
}

// ExtractFile reads a .pptx from disk and extracts its Deck. If no document or
// first-slide title is found, the title falls back to the filename stem.
func ExtractFile(filename string) (Deck, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Deck{}, err
	}
	deck, err := Extract(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return deck, err
	}
	if deck.Title == "" {
		base := filepath.Base(filename)
		deck.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return deck, nil
}

// Extract parses a .pptx zip into a Deck, with slides in presentation order.
func Extract(r io.ReaderAt, size int64) (Deck, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Deck{}, err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	// decode streams a part straight through the XML decoder, avoiding a
	// full-size []byte copy per part — the dominant allocation on large decks.
	decode := func(name string, v any) error {
		f, ok := files[name]
		if !ok {
			return fmt.Errorf("missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		return xml.NewDecoder(rc).Decode(v)
	}

	var pres presentationXML
	if err := decode("ppt/presentation.xml", &pres); err != nil {
		return Deck{}, err
	}
	if len(pres.SldIDLst) == 0 {
		return Deck{}, errors.New("presentation has no slides")
	}

	presRelsData, err := read("ppt/_rels/presentation.xml.rels")
	if err != nil {
		return Deck{}, err
	}
	presRels, err := parseRelationships(presRelsData, "ppt")
	if err != nil {
		return Deck{}, err
	}

	deck := Deck{Title: deckTitle(read)}
	for i, sid := range pres.SldIDLst {
		rel, ok := presRels[sid.RelID]
		if !ok || !strings.Contains(rel.Type, "/slide") {
			continue
		}
		slide, err := extractSlide(decode, read, rel.Target, i+1)
		if err != nil {
			return Deck{}, err
		}
		if deck.Title == "" && slide.Title != "" {
			deck.Title = slide.Title
		}
		deck.Slides = append(deck.Slides, slide)
	}
	return deck, nil
}

// deckTitle reads docProps/core.xml <dc:title>; empty if absent.
func deckTitle(read func(string) ([]byte, error)) string {
	data, err := read("docProps/core.xml")
	if err != nil {
		return ""
	}
	var core struct {
		Title string `xml:"title"`
	}
	if err := xml.Unmarshal(data, &core); err != nil {
		return ""
	}
	return strings.TrimSpace(core.Title)
}

func extractSlide(decode func(string, any) error, read func(string) ([]byte, error), slidePath string, num int) (Slide, error) {
	var sx slideXML
	if err := decode(slidePath, &sx); err != nil {
		return Slide{}, err
	}

	rels := map[string]relationship{}
	if relsData, rErr := read(relsPathFor(slidePath)); rErr == nil {
		var err error
		rels, err = parseRelationships(relsData, path.Dir(slidePath))
		if err != nil {
			return Slide{}, err
		}
	}

	slide := Slide{Number: num}
	titleSet := false
	for _, sh := range sx.Shapes {
		blocks := paragraphsToBlocks(sh.Body)
		if len(blocks) == 0 {
			continue
		}
		if !titleSet && isTitlePH(sh.NV.NvPr.PH) {
			slide.Title = blockText(blocks)
			titleSet = true
			continue
		}
		slide.Blocks = append(slide.Blocks, blocks...)
	}
	for _, pic := range sx.Pics {
		slide.Blocks = append(slide.Blocks, Block{Type: "image", Alt: firstNonEmpty(pic.NV.CNvPr.Descr, pic.NV.CNvPr.Name)})
	}
	for _, gf := range sx.Graphics {
		if gf.Table != nil {
			slide.Blocks = append(slide.Blocks, Block{Type: "table", Rows: tableRows(gf.Table)})
		}
	}
	if noteRel := findRelByType(rels, relTypeNotes); noteRel.Target != "" {
		var nsx slideXML
		if decode(noteRel.Target, &nsx) == nil {
			slide.Notes = notesTextFromSlide(nsx)
		}
	}
	return slide, nil
}

func isTitlePH(ph *phXML) bool {
	return ph != nil && (ph.Type == "title" || ph.Type == "ctrTitle" || ph.Type == "subTitle")
}

func paragraphsToBlocks(body *txBodyXML) []Block {
	if body == nil {
		return nil
	}
	var out []Block
	for _, p := range body.Paragraphs {
		text := strings.TrimSpace(paragraphText(p))
		if text == "" {
			continue
		}
		kind := "paragraph"
		level := 0
		if p.PPr != nil {
			kind = "bullet"
			level = max(0, p.PPr.Level)
		}
		out = append(out, Block{Type: kind, Text: text, Level: level})
	}
	return out
}

func paragraphText(p paragraphXML) string {
	var parts []string
	for _, r := range p.Runs {
		if r.Text != "" {
			parts = append(parts, r.Text)
		}
	}
	for _, f := range p.Flds {
		if f.Text != "" {
			parts = append(parts, f.Text)
		}
	}
	return strings.Join(parts, "")
}

func blockText(blocks []Block) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, " ")
}

func tableRows(t *tableXML) [][]string {
	var rows [][]string
	for _, r := range t.Rows {
		cells := make([]string, 0, len(r.Cells))
		for _, c := range r.Cells {
			cell := c
			cells = append(cells, blockText(paragraphsToBlocks(&cell.Body)))
		}
		rows = append(rows, cells)
	}
	return rows
}

// notesText extracts the speaker-notes text, skipping the slide-number placeholder.
func notesText(data []byte) string {
	var sx slideXML
	if err := xml.Unmarshal(data, &sx); err != nil {
		return ""
	}
	return notesTextFromSlide(sx)
}

// notesTextFromSlide extracts speaker-notes text from an already-decoded notes
// slide, skipping the slide-number placeholder.
func notesTextFromSlide(sx slideXML) string {
	var parts []string
	for _, sh := range sx.Shapes {
		if sh.NV.NvPr.PH != nil && sh.NV.NvPr.PH.Type == "sldNum" {
			continue
		}
		for _, b := range paragraphsToBlocks(sh.Body) {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

func findRelByType(rels map[string]relationship, typ string) relationship {
	keys := make([]string, 0, len(rels))
	for k := range rels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if rels[k].Type == typ {
			return rels[k]
		}
	}
	return relationship{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return "image"
}
