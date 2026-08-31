package pptx

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildZip assembles an in-memory .pptx-like zip from name→content parts.
func buildZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func slideWithTitle(title string) string {
	return `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="1" name="Title"/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>` + title + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
}

// Slides must follow presentation order (sldIdLst), not lexical filename order.
func TestSlideOrderFollowsSldIdLst(t *testing.T) {
	parts := map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId r:id="rIdB"/><p:sldId r:id="rIdA"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdA" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="rIdB" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           slideWithTitle("Alpha"),
		"ppt/slides/slide2.xml":           slideWithTitle("Bravo"),
	}
	data := buildZip(t, parts)
	deck, err := Extract(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(deck.Slides) != 2 {
		t.Fatalf("slides = %d", len(deck.Slides))
	}
	if deck.Slides[0].Title != "Bravo" || deck.Slides[1].Title != "Alpha" {
		t.Fatalf("order = %q, %q", deck.Slides[0].Title, deck.Slides[1].Title)
	}
}

func TestParagraphsToBlocksLevels(t *testing.T) {
	body := &txBodyXML{Paragraphs: []paragraphXML{
		{Runs: []runXML{{Text: "top"}}, PPr: &pPrXML{Level: 0}},
		{Runs: []runXML{{Text: "nested"}}, PPr: &pPrXML{Level: 1}},
		{Runs: []runXML{{Text: "plain"}}},
	}}
	blocks := paragraphsToBlocks(body)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	if blocks[0].Type != "bullet" || blocks[0].Level != 0 {
		t.Fatalf("b0 = %+v", blocks[0])
	}
	if blocks[1].Type != "bullet" || blocks[1].Level != 1 {
		t.Fatalf("b1 = %+v", blocks[1])
	}
	if blocks[2].Type != "paragraph" {
		t.Fatalf("b2 = %+v", blocks[2])
	}
}

// The fix vs the POC: the slide-number placeholder must not leak into notes.
func TestNotesTextSkipsSlideNumber(t *testing.T) {
	notes := []byte(`<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
		`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Notes"/><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>Real note</a:t></a:r></a:p></p:txBody></p:sp>` +
		`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Num"/><p:nvPr><p:ph type="sldNum"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>7</a:t></a:r></a:p></p:txBody></p:sp>` +
		`</p:spTree></p:cSld></p:notes>`)
	if got := notesText(notes); got != "Real note" {
		t.Fatalf("notes = %q", got)
	}
}

func TestExtractBasicDeck(t *testing.T) {
	deck, err := ExtractFile("testdata/fixtures/basic.pptx")
	if err != nil {
		t.Fatal(err)
	}
	if deck.Title != "Quarterly Review" {
		t.Fatalf("title = %q", deck.Title)
	}
	if len(deck.Slides) != 2 {
		t.Fatalf("slides = %d", len(deck.Slides))
	}
	if deck.Slides[0].Title != "Quarterly Review" {
		t.Fatalf("slide1 title = %q", deck.Slides[0].Title)
	}
	if deck.Slides[1].Title != "Roadmap" {
		t.Fatalf("slide2 title = %q", deck.Slides[1].Title)
	}
	if !strings.Contains(deck.Slides[0].Notes, "Europe") {
		t.Fatalf("notes = %q", deck.Slides[0].Notes)
	}
}
