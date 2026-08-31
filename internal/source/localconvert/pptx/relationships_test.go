package pptx

import "testing"

func TestParseRelationshipsRelative(t *testing.T) {
	data := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
		<Relationship Id="rId1" Type="http://x/slide" Target="slides/slide1.xml"/>
	</Relationships>`)
	rels, err := parseRelationships(data, "ppt")
	if err != nil {
		t.Fatal(err)
	}
	if rels["rId1"].Target != "ppt/slides/slide1.xml" {
		t.Fatalf("target = %q", rels["rId1"].Target)
	}
}

func TestParseRelationshipsAbsolute(t *testing.T) {
	data := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
		<Relationship Id="rId2" Type="http://x/image" Target="/ppt/media/image1.png"/>
	</Relationships>`)
	rels, err := parseRelationships(data, "ppt/slides")
	if err != nil {
		t.Fatal(err)
	}
	if rels["rId2"].Target != "ppt/media/image1.png" {
		t.Fatalf("target = %q", rels["rId2"].Target)
	}
}

func TestRelsPathFor(t *testing.T) {
	if got := relsPathFor("ppt/slides/slide1.xml"); got != "ppt/slides/_rels/slide1.xml.rels" {
		t.Fatalf("got %q", got)
	}
}
