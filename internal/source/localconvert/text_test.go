package localconvert

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestConvertTextToMarkdown_UTF8Passthrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := "# 标题\n\n正文内容。"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := ConvertTextToMarkdown(path)
	if err != nil {
		t.Fatalf("ConvertTextToMarkdown: %v", err)
	}
	if string(got) != content {
		t.Fatalf("expected passthrough, got:\n%s", got)
	}
}

func TestConvertTextToMarkdown_StripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	content := "纯文本内容"
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(content)...)
	if err := os.WriteFile(path, withBOM, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := ConvertTextToMarkdown(path)
	if err != nil {
		t.Fatalf("ConvertTextToMarkdown: %v", err)
	}
	if string(got) != content {
		t.Fatalf("expected BOM stripped, got %q", got)
	}
}

func TestConvertTextToMarkdown_GBKFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	content := "考勤管理制度说明"
	gbkBytes, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(content))
	if err != nil {
		t.Fatalf("encode fixture as GBK: %v", err)
	}
	if err := os.WriteFile(path, gbkBytes, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := ConvertTextToMarkdown(path)
	if err != nil {
		t.Fatalf("ConvertTextToMarkdown: %v", err)
	}
	if string(got) != content {
		t.Fatalf("expected GBK-decoded UTF-8 %q, got %q", content, got)
	}
}

func TestLocalConvertClient_ConvertToMarkdown_TextFormats(t *testing.T) {
	dir := t.TempDir()
	c := NewLocalConvertClient()
	for _, ext := range []string{".md", ".markdown", ".txt"} {
		path := filepath.Join(dir, "file"+ext)
		if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		got, err := c.ConvertToMarkdown(t.Context(), path)
		if err != nil {
			t.Fatalf("ConvertToMarkdown(%s): %v", ext, err)
		}
		if string(got) != "hello" {
			t.Fatalf("ConvertToMarkdown(%s): expected passthrough, got %q", ext, got)
		}
	}
}
