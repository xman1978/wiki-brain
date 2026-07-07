package textmatch

import (
	"strings"
	"testing"
)

func TestMatchFragment_Exact(t *testing.T) {
	content := "line one\nline two is the answer\nline three"
	start, end, matched, ok := MatchFragment(content, "line two is the answer")
	if !ok {
		t.Fatal("expected exact match")
	}
	if matched != "line two is the answer" {
		t.Errorf("matched = %q", matched)
	}
	if content[start:end] != matched {
		t.Errorf("content[%d:%d] = %q, want %q", start, end, content[start:end], matched)
	}
}

func TestMatchFragment_FuzzyWhitespace(t *testing.T) {
	// Original has irregular spacing/indentation the model might normalize.
	content := "步骤一：  先做 A\n  然后做   B\n步骤二：做 C"
	fragment := "先做 A 然后做 B" // model collapsed the newline+indent into single spaces
	start, end, matched, ok := MatchFragment(content, fragment)
	if !ok {
		t.Fatal("expected fuzzy match to succeed")
	}
	if matched != content[start:end] {
		t.Errorf("matched should equal the original-text slice it reports")
	}
	// content must be taken from the original (with its real newline/indentation),
	// not the model's collapsed rendering.
	if !strings.Contains(matched, "\n") {
		t.Errorf("expected matched original text to retain its newline, got %q", matched)
	}
}

func TestMatchFragment_Hallucinated(t *testing.T) {
	content := "the quick brown fox"
	_, _, _, ok := MatchFragment(content, "a sentence that never appears here")
	if ok {
		t.Error("expected hallucinated fragment to not match")
	}
}

func TestByteRangeToLines_Invariant(t *testing.T) {
	content := "line1\nline2\nline3\nline4"
	// "line2\nline3" spans relative lines 2-3.
	start := strings.Index(content, "line2\nline3")
	end := start + len("line2\nline3")
	lineStart, lineEnd := ByteRangeToLines(content, start, end)
	if lineStart != 2 || lineEnd != 3 {
		t.Fatalf("lineStart=%d lineEnd=%d, want 2,3", lineStart, lineEnd)
	}

	// Verification invariant: joining the resolved line range must contain
	// the matched fragment text (docs/impl/v1/evidence.md 步骤 3, "验证不变式").
	lines := strings.Split(content, "\n")
	joined := strings.Join(lines[lineStart-1:lineEnd], "\n")
	if !strings.Contains(joined, content[start:end]) {
		t.Errorf("invariant violated: %q does not contain %q", joined, content[start:end])
	}
}

func TestByteRangeToLines_SingleLine(t *testing.T) {
	content := "alpha\nbeta gamma delta\nepsilon"
	start := strings.Index(content, "gamma")
	end := start + len("gamma")
	lineStart, lineEnd := ByteRangeToLines(content, start, end)
	if lineStart != 2 || lineEnd != 2 {
		t.Fatalf("lineStart=%d lineEnd=%d, want 2,2", lineStart, lineEnd)
	}
}
