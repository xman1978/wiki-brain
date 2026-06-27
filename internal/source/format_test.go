package source

import "testing"

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		fileName string
		want     string
	}{
		{"doc.md", "markdown"},
		{"doc.markdown", "markdown"},
		{"doc.pdf", "pdf"},
		{"doc.docx", "word"},
		{"photo.jpg", "image"},
		{"photo.PNG", "image"},
		{"unknown.xyz", "other"},
	}
	for _, tt := range tests {
		got := DetectFormat(tt.fileName)
		if got != tt.want {
			t.Errorf("DetectFormat(%q) = %q, want %q", tt.fileName, got, tt.want)
		}
	}
}

func TestIsMarkdown(t *testing.T) {
	if !IsMarkdown("test.md") {
		t.Error("test.md should be markdown")
	}
	if !IsMarkdown("TEST.MARKDOWN") {
		t.Error("TEST.MARKDOWN should be markdown")
	}
	if IsMarkdown("test.pdf") {
		t.Error("test.pdf should not be markdown")
	}
}

func TestIsSupportedFormat(t *testing.T) {
	if !IsSupportedFormat("test.pdf") {
		t.Error("pdf should be supported")
	}
	if IsSupportedFormat("test.xyz") {
		t.Error("xyz should not be supported")
	}
}
