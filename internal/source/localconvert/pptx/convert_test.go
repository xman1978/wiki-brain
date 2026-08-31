package pptx

import (
	"strings"
	"testing"
)

func TestConvert(t *testing.T) {
	md, err := Convert("testdata/fixtures/basic.pptx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "## Slide 1: Quarterly Review") {
		t.Fatalf("unexpected output:\n%s", md)
	}
}
