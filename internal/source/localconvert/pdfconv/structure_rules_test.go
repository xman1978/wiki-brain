package pdfconv

import "testing"

// TestIsListItem ports the concrete distinguishing cases discovered while
// porting PdfToMarkdown.isListItem faithfully from the real Java source
// (see fixture 5e93ff0e-591e-4ef6-ace0-eacd7865419c.docx and
// f91b4c13-535c-41ee-9e28-e31f0220c804.docx, which motivated this port).
func TestIsListItem(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// 5e93ff0e: digit + full-width-paren field-label lines are real
		// list items and must be suppressed as heading candidates. Body
		// "支付条件：" ends with "：" so the section-title-body exception
		// (isHeadingByRegex && looksLikeSectionTitleBody) does not fire.
		{"digit-fullwidth-paren field label", "1）支付条件：", true},
		{"digit-dot field label with colon", "2、验收标准：", true},
		// f91b4c13: a bare Latin-letter prefix ("a.", "b.", "c.") is never
		// matched by LIST_BULLET or LIST_NUM_PREFIX (both digit/bullet-only
		// in the real Java regexes) — isListItem must return false so these
		// remain eligible heading candidates (TITLE_ALPHA).
		{"alpha heading not a list item", "a. 在域控制器上创建相关账户", false},
		{"alpha heading not a list item (b)", "b. 设置服务的连接时间", false},
		// Short numbered section titles ("1. 总则") must NOT be treated as
		// list items: body "总则" is short and has none of the
		// sentence-punctuation characters, so looksLikeSectionTitleBody
		// fires the isListItem exception.
		{"short numbered section title", "1. 总则", false},
		{"short numbered section title 2", "2. 服务器", false},
		// A genuinely long/punctuated numbered body is a real list item.
		{"long numbered body with punctuation", "1. 将 grid 安装包解压到安装目录，并检查权限是否正确。", true},
		// Bullet markers are always list items regardless of body.
		{"bullet marker", "- 这是一个列表项", true},
		{"bullet marker star (Java LIST_BULLET includes ★)", "★ 重点关注事项", true},
		// Decimal numbers must not be misread as "digit + separator".
		{"decimal number is not a list prefix", "98.5 分为合格", false},
		// Blank / empty.
		{"empty string", "", false},
		{"whitespace only", "   ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsListItem(c.in)
			if got != c.want {
				t.Errorf("IsListItem(%q) = %v, want %v", c.in, got, c.want)
			}
			// IsOrderedListItemLine is a pure delegation in the real Java
			// source (MarkdownStructureRules.isOrderedListItemLine ->
			// PdfToMarkdown.isListItem) and must always agree.
			if got2 := IsOrderedListItemLine(c.in); got2 != got {
				t.Errorf("IsOrderedListItemLine(%q) = %v, want %v (must match IsListItem)", c.in, got2, got)
			}
		})
	}
}

// TestIsHeadingByRegexLiteral checks the literal PdfToMarkdown.isHeadingByRegex
// port against representative cases for each of the 9 TITLE_* patterns,
// including the two negative-lookahead patterns (TITLE_NUM_SIMPLE,
// TITLE_NUM_MULTI) that need the find-then-check-boundary workaround.
func TestIsHeadingByRegexLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"一、总体要求", true},  // TITLE_CN_NUM
		{"（一）基本原则", true}, // TITLE_CN_PAREN
		{"第一章 总则", true},  // TITLE_CHAPTER
		{"1. 总则", true},   // TITLE_NUM_SIMPLE
		// TITLE_NUM_SIMPLE's own boundary check rejects "1，2份文件" (separator
		// "，" immediately followed by digit "2"); the comma separator means
		// TITLE_NUM_MULTI (which requires a literal ".") cannot match either,
		// so this is a clean negative case isolating the lookahead check.
		{"1，2份文件", false},
		{"1.2.3 条款", true},        // TITLE_NUM_MULTI
		{"1.2.3.4% 折扣", false},    // TITLE_NUM_MULTI boundary: trailing "%" must not match
		{"(1) 总则", true},          // TITLE_NUM_PAREN
		{"1)总则", true},            // TITLE_NUM_SUFFIX
		{"I. 引言", true},           // TITLE_ROMAN
		{"a. 在域控制器上创建相关账户", true}, // TITLE_ALPHA
		{"这是一段普通正文，不是标题。", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isHeadingByRegex(c.in); got != c.want {
			t.Errorf("isHeadingByRegex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestLooksLikeSectionTitleBodyLiteral checks the literal
// PdfToMarkdown.looksLikeSectionTitleBody port: <=18 runes and none of the
// sentence-punctuation characters "，。；：,.!?;:" anywhere in the string
// (not just at the end — this differs from EndsWithTerminalPunctuation).
func TestLooksLikeSectionTitleBodyLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"总则", true},
		{"支付条件：", false},        // trailing 。-class punctuation anywhere disqualifies
		{"这是一句包含逗号，的短语", false}, // internal punctuation disqualifies even though short
		{"一个不短不短不短不短不短不短不短不短的短语超过十八个字符了", false}, // >18 runes
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeSectionTitleBody(c.in); got != c.want {
			t.Errorf("looksLikeSectionTitleBody(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
