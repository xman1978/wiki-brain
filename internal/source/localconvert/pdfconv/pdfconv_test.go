package pdfconv

import "testing"

func TestClassifyPrefixKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"第一章 总则", "TITLE_CHAPTER_ONE"},
		{"第五条 适用范围", "TITLE_CHAPTER_FIVE"},
		{"（一）基本原则", "TITLE_CN_PAREN"},
		{"一、总体要求", "TITLE_CN_NUM"},
		{"1.2.3.4.5 详细条款", "TITLE_NUM_FIVE"},
		{"1.2.3.4 详细条款", "TITLE_NUM_FOUR"},
		{"1.2.3 条款", "TITLE_NUM_THREE"},
		{"1.2 条款", "TITLE_NUM_TOW"},
		{"1. 总则", "TITLE_NUM_DOT"},
		{"1、总则", "TITLE_NUM_DUNHAO"},
		{"1)总则", "TITLE_NUM_SUFFIX"},
		{"(1) 总则", "TITLE_NUM_PAREN"},
		{"I. 引言", "TITLE_ROMAN"},
		{"A. 附录", "TITLE_ALPHA"},
		{"这是一段普通正文，不是标题。", ""},
		// negative-lookahead boundary cases (pdf-port/04 兼容性预警)
		{"1.20-30 之间", ""}, // "1." would falsely match TITLE_NUM_DOT if boundary check missing
		{"1.2.3 更长编号", "TITLE_NUM_THREE"},
	}
	for _, c := range cases {
		got := ClassifyPrefixKey(c.in)
		if got != c.want {
			t.Errorf("ClassifyPrefixKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNaturalLevelForTitle(t *testing.T) {
	if lv := NaturalLevelForTitle("第一章 总则"); lv != 1 {
		t.Errorf("chapter one level = %d, want 1", lv)
	}
	if lv := NaturalLevelForTitle("第五条 适用范围"); lv != 2 {
		t.Errorf("chapter five level = %d, want 2", lv)
	}
	if lv := NaturalLevelForTitle("一、总体要求"); lv != 3 {
		t.Errorf("cn num level = %d, want 3", lv)
	}
	if lv := NaturalLevelForTitle("1.2 条款"); lv != 3 {
		t.Errorf("num tow level = %d, want 3", lv)
	}
	if lv := NaturalLevelForTitle("1. 总则"); lv != 4 {
		t.Errorf("num dot level = %d, want 4", lv)
	}
	if lv := NaturalLevelForTitle("普通正文"); lv != 0 {
		t.Errorf("plain text level = %d, want 0", lv)
	}
}

func TestIsChapterTocLine(t *testing.T) {
	if !IsChapterTocLine("第一章 总则..........................1") {
		t.Error("expected TOC line to be recognized")
	}
	if IsChapterTocLine("第一章 总则") {
		t.Error("plain structural heading must not be a TOC line")
	}
}

func TestIsStructuralChapterHeading(t *testing.T) {
	if !IsStructuralChapterHeading("第一章 总则") {
		t.Error("expected structural chapter heading")
	}
	if IsStructuralChapterHeading("第一章 总则..........................1") {
		t.Error("TOC line must not be a structural heading")
	}
}

func TestClearlyFailsHeadingQuality(t *testing.T) {
	if !ClearlyFailsHeadingQuality("本条款适用于所有投标人，且投标人应当在响应文件中明确说明其资质等级和业绩情况。") {
		t.Error("long sentence with terminal punctuation should fail heading quality")
	}
	if ClearlyFailsHeadingQuality("第一章 总则") {
		t.Error("short structural heading should not fail")
	}
}

func TestWrapCodeFence(t *testing.T) {
	got := WrapCodeFence([]string{"select * from t", "where x = 1"})
	want := "```\nselect * from t\nwhere x = 1\n```\n"
	if got != want {
		t.Errorf("WrapCodeFence = %q, want %q", got, want)
	}
}

func TestLooksLikePreformattedBlock(t *testing.T) {
	if !LooksLikePreformattedBlock([]string{"select * from users", "where id = 1"}) {
		t.Error("expected SQL snippet to be recognized as preformatted")
	}
	if LooksLikePreformattedBlock([]string{"这是一段普通的中文说明文字，介绍了本章节的主要内容和背景。", "第二句同样是自然语言，没有任何代码特征。"}) {
		t.Error("plain Chinese prose must not be classified as preformatted")
	}
}

func TestDetectMarkdownLinesToDemote(t *testing.T) {
	lines := []string{
		"# 1、总则",
		"正文内容",
		"2、范围",
		"正文内容",
	}
	recognized := func(i int) bool { return i == 0 }
	demoted := DetectMarkdownLinesToDemote(lines, recognized)
	if !demoted[0] {
		t.Errorf("expected line 0 to be demoted (mixed recognition segment), got %v", demoted)
	}
}
