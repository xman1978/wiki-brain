package trace

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  Hello, World!  ", "hello world"},
		{"什么是  锁？", "什么是 锁"},
		{"UPPER  lower", "upper lower"},
		{"Go  1.21!!", "go 121"},
		{"  ", ""},
	}
	for _, tt := range tests {
		got := normalize(tt.input)
		if got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuestionHash_SameForSameNormalized(t *testing.T) {
	h1 := questionHash(normalize("什么是锁？"))
	h2 := questionHash(normalize("什么是锁"))
	if h1 != h2 {
		t.Errorf("same normalized question should produce same hash: %s vs %s", h1, h2)
	}
}

func TestQuestionHash_DifferentForDifferent(t *testing.T) {
	h1 := questionHash(normalize("什么是锁"))
	h2 := questionHash(normalize("锁是什么"))
	if h1 == h2 {
		t.Errorf("different questions should produce different hashes")
	}
}

func TestQuestionHash_Length(t *testing.T) {
	h := questionHash("test")
	if len(h) != 32 {
		t.Errorf("hash length should be 32 hex chars, got %d", len(h))
	}
}

func TestQuestionTerms_GseSegmentation(t *testing.T) {
	terms := questionTerms(normalize("什么是绩效管理"))
	t.Logf("terms: %q", terms)
	// gse should produce multi-char words like "绩效" "管理", not single chars
	if terms == "" {
		t.Error("expected non-empty terms")
	}
	// "什么" and "是" are stop words, should be filtered
	for _, sw := range []string{"什么", "是"} {
		found := false
		for _, w := range splitTerms(terms) {
			if w == sw {
				found = true
			}
		}
		if found {
			t.Errorf("stop word %q should be filtered, got terms=%q", sw, terms)
		}
	}
	// Should contain "绩效" or "管理" as multi-char words
	hasMultiChar := false
	for _, w := range splitTerms(terms) {
		if len([]rune(w)) > 1 {
			hasMultiChar = true
			break
		}
	}
	if !hasMultiChar {
		t.Errorf("expected multi-char CJK words from gse, got terms=%q", terms)
	}
}

func TestQuestionTerms_Sorted(t *testing.T) {
	terms := questionTerms("golang database lock")
	if terms != "database golang lock" {
		t.Errorf("expected sorted terms, got %q", terms)
	}
}

func TestQuestionTerms_Truncation(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "verylongword "
	}
	terms := questionTerms(long)
	if len(terms) > 200 {
		t.Errorf("terms should be truncated to 200 chars, got %d", len(terms))
	}
}

func TestTokenize_MixedCJKAndLatin(t *testing.T) {
	tokens := tokenize("go语言并发锁")
	t.Logf("tokens: %v", tokens)
	if len(tokens) < 2 {
		t.Errorf("expected multiple tokens, got %v", tokens)
	}
	if tokens[0] != "go" {
		t.Errorf("first token should be 'go', got %q", tokens[0])
	}
}

func TestTokenize_PureChineseProducesWords(t *testing.T) {
	tokens := tokenize("绩效管理考核制度")
	t.Logf("tokens: %v", tokens)
	hasMultiChar := false
	for _, tok := range tokens {
		if len([]rune(tok)) > 1 {
			hasMultiChar = true
			break
		}
	}
	if !hasMultiChar {
		t.Errorf("gse should produce multi-char words, got %v", tokens)
	}
}

func splitTerms(s string) []string {
	var result []string
	for _, w := range strings.Split(s, " ") {
		if w != "" {
			result = append(result, w)
		}
	}
	return result
}
