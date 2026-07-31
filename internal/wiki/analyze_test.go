package wiki

import "testing"

// TestCorrectAspectIDs_FallsBackToCitedPointsMajorityAspect covers
// docs/impl/v1/wiki-generation.md 3.4: a claim whose aspect_id is empty or
// unknown gets corrected from whichever aspect its cited_point_ids mostly
// belong to.
func TestCorrectAspectIDs_FallsBackToCitedPointsMajorityAspect(t *testing.T) {
	aspects := []Aspect{
		{AspectID: "a1", PointIDs: []string{"p1", "p2"}},
		{AspectID: "a2", PointIDs: []string{"p3"}},
	}
	pointAspect := map[string]string{"p1": "a1", "p2": "a1", "p3": "a2"}

	claims := []Claim{
		{Summary: "unknown aspect, majority a1", CitedPointIDs: []string{"p1", "p2"}, AspectID: "does-not-exist"},
		{Summary: "empty aspect, single cite a2", CitedPointIDs: []string{"p3"}, AspectID: ""},
		{Summary: "already valid, left alone", CitedPointIDs: []string{"p3"}, AspectID: "a2"},
		{Summary: "tie between a1/a2 falls back to misc", CitedPointIDs: []string{"p1", "p3"}, AspectID: ""},
		{Summary: "cites nothing known, falls back to misc", CitedPointIDs: []string{"p999"}, AspectID: ""},
	}

	correctAspectIDs(claims, aspects, pointAspect)

	want := []string{"a1", "a2", "a2", aspectMiscID, aspectMiscID}
	for i, w := range want {
		if claims[i].AspectID != w {
			t.Errorf("claim %d (%q): AspectID = %q, want %q", i, claims[i].Summary, claims[i].AspectID, w)
		}
	}
}

func TestExtractSection_PullsTextBetweenHeadings(t *testing.T) {
	content := "## 摘要\n\n这是摘要文本。\n\n## 稳定结论\n\n结论内容 [p1]\n"
	got := extractSection(content, "## 摘要")
	if got != "这是摘要文本。" {
		t.Errorf("extractSection = %q, want %q", got, "这是摘要文本。")
	}
}
