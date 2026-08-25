package trace

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

func TestGradeQuality_Confident(t *testing.T) {
	r := &answer.AnswerResult{
		Citations: []string{"f1", "f2"},
		EvidenceSet: &retrieval.EvidenceSet{
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
				{FactID: "f2", PointID: "p2"},
			},
		},
	}
	g := gradeQuality(r)
	if g.Quality != QualityConfident {
		t.Errorf("expected confident, got %s", g.Quality)
	}
	if len(g.DirectPointIDs) != 2 {
		t.Errorf("expected 2 direct point ids, got %d", len(g.DirectPointIDs))
	}
}

// A citation landing only in Supporting (not DirectEvidence) is still a
// confident, correctly-resolved answer — rerank's direct/supporting split is
// a rank bucket, not a correctness judgment (see directCitedPointIDs doc
// comment) — so this grades confident, not partial.
func TestGradeQuality_CitedSupportingOnly_Confident(t *testing.T) {
	r := &answer.AnswerResult{
		Citations: []string{"f3"},
		EvidenceSet: &retrieval.EvidenceSet{
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
			Supporting: []retrieval.Evidence{
				{FactID: "f3", PointID: "p3"},
			},
		},
	}
	g := gradeQuality(r)
	if g.Quality != QualityConfident {
		t.Errorf("expected confident, got %s", g.Quality)
	}
	if len(g.DirectPointIDs) != 1 || g.DirectPointIDs[0] != "p3" {
		t.Errorf("expected DirectPointIDs=[p3], got %v", g.DirectPointIDs)
	}
}

// Partial still applies when nothing cited resolves to either bucket but
// Supporting evidence exists (the true "cited something we don't recognize,
// but retrieval found related material" case).
func TestGradeQuality_Partial(t *testing.T) {
	r := &answer.AnswerResult{
		Citations: []string{"unknown"},
		EvidenceSet: &retrieval.EvidenceSet{
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
			Supporting: []retrieval.Evidence{
				{FactID: "f3", PointID: "p3"},
			},
		},
	}
	g := gradeQuality(r)
	if g.Quality != QualityPartial {
		t.Errorf("expected partial, got %s", g.Quality)
	}
}

func TestGradeQuality_Gap_NoEvidence(t *testing.T) {
	r := &answer.AnswerResult{
		Citations:   []string{},
		EvidenceSet: &retrieval.EvidenceSet{},
	}
	g := gradeQuality(r)
	if g.Quality != QualityGap {
		t.Errorf("expected gap, got %s", g.Quality)
	}
}

func TestGradeQuality_Gap_NilEvidenceSet(t *testing.T) {
	r := &answer.AnswerResult{EvidenceSet: nil}
	g := gradeQuality(r)
	if g.Quality != QualityGap {
		t.Errorf("expected gap, got %s", g.Quality)
	}
}

// DirectEvidence exists but wasn't cited; what was cited (f99) resolves via
// Supporting instead — still confident under the union lookup.
func TestGradeQuality_DirectNotCited_SupportingCited_Confident(t *testing.T) {
	r := &answer.AnswerResult{
		Citations: []string{"f99"},
		EvidenceSet: &retrieval.EvidenceSet{
			DirectEvidence: []retrieval.Evidence{
				{FactID: "f1", PointID: "p1"},
			},
			Supporting: []retrieval.Evidence{
				{FactID: "f99", PointID: "p99"},
			},
		},
	}
	g := gradeQuality(r)
	if g.Quality != QualityConfident {
		t.Errorf("expected confident, got %s", g.Quality)
	}
	if len(g.DirectPointIDs) != 1 || g.DirectPointIDs[0] != "p99" {
		t.Errorf("expected DirectPointIDs=[p99], got %v", g.DirectPointIDs)
	}
}

func TestDirectCitedPointIDs_Dedup(t *testing.T) {
	es := &retrieval.EvidenceSet{
		DirectEvidence: []retrieval.Evidence{
			{FactID: "f1", PointID: "p1"},
			{FactID: "f2", PointID: "p1"},
		},
	}
	result := directCitedPointIDs(es, []string{"f1", "f2"})
	if len(result) != 1 {
		t.Errorf("expected 1 unique point id, got %d", len(result))
	}
}
