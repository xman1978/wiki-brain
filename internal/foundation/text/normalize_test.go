package text

import "testing"

func TestNormalize_CollapsesWhitespace(t *testing.T) {
	got := Normalize("  Hello,   World!  ")
	if got != "hello world" {
		t.Errorf("Normalize = %q, want %q", got, "hello world")
	}
}

func TestNormalizeCompact_StripsWhitespaceEntirely(t *testing.T) {
	got := NormalizeCompact("HR 部门")
	if got != "hr部门" {
		t.Errorf("NormalizeCompact = %q, want %q", got, "hr部门")
	}
}

func TestNormalizeCompact_DiffersFromNormalizeOnSpacing(t *testing.T) {
	raw := "产品 经理"
	if Normalize(raw) == NormalizeCompact(raw) {
		t.Errorf("expected Normalize (collapses to single space) and NormalizeCompact (strips entirely) to differ for %q", raw)
	}
	if NormalizeCompact(raw) != "产品经理" {
		t.Errorf("NormalizeCompact = %q, want 产品经理", NormalizeCompact(raw))
	}
}

func TestTerms_SortsFiltersAndJoins(t *testing.T) {
	got := Terms("golang database lock is the")
	if got != "database golang lock" {
		t.Errorf("Terms = %q, want %q", got, "database golang lock")
	}
}

func TestTermSet_NormalizesTokenizesAndFilters(t *testing.T) {
	set := TermSet("What is 绩效管理?")
	if _, ok := set["what"]; ok {
		t.Error("stop word 'what' should be filtered from TermSet")
	}
	if len(set) == 0 {
		t.Error("expected non-empty term set")
	}
}

func TestSplitTerms_EmptyString(t *testing.T) {
	set := SplitTerms("")
	if len(set) != 0 {
		t.Errorf("expected empty set for empty string, got %v", set)
	}
}

func TestSplitTerms_RoundTripsWithTerms(t *testing.T) {
	terms := Terms(Normalize("apple banana cherry"))
	set := SplitTerms(terms)
	for _, w := range []string{"apple", "banana", "cherry"} {
		if _, ok := set[w]; !ok {
			t.Errorf("expected %q in split set, got %v", w, set)
		}
	}
}
