package wiki

import "testing"

func TestExtractSection_PullsTextBetweenHeadings(t *testing.T) {
	content := "## 摘要\n\n这是摘要文本。\n\n## 稳定结论\n\n结论内容 [p1]\n"
	got := extractSection(content, "## 摘要")
	if got != "这是摘要文本。" {
		t.Errorf("extractSection = %q, want %q", got, "这是摘要文本。")
	}
}
