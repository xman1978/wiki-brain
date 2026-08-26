package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// TestSubjectNormalizer_TierExactAndNewRecord covers the two cheap tiers: a
// never-seen subject inserts a new canonical row (Tier 4), and asking again
// with the exact same normalized text hits it via Tier 1 without needing the
// LLM tier at all (no fake response configured for subject_norm_match.md).
func TestSubjectNormalizer_TierExactAndNewRecord(t *testing.T) {
	_, _, store := setupTestService(t)
	norm := NewSubjectNormalizer(store, SubjectNormConfig{})

	got, err := norm.Normalize(context.Background(), []string{"d1"}, "线性方程")
	if err != nil {
		t.Fatal(err)
	}
	if got != text.Normalize("线性方程") {
		t.Fatalf("expected normalized subject, got %q", got)
	}

	rows, err := store.ListSubjectNormCandidates([]string{"d1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 subject_norms row after first ask, got %d", len(rows))
	}

	got2, err := norm.Normalize(context.Background(), []string{"d1"}, "线性方程")
	if err != nil {
		t.Fatal(err)
	}
	if got2 != got {
		t.Fatalf("expected exact-match tier to return the same canonical subject, got %q vs %q", got2, got)
	}

	rows2, err := store.ListSubjectNormCandidates([]string{"d1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 1 {
		t.Fatalf("expected still 1 subject_norms row (no duplicate insert on exact re-ask), got %d", len(rows2))
	}
}

// TestSourceAffinityShortcut_HitSkipsDomainAndSourceFilter seeds a binding
// directly (as if Trace had already confirmed it) and asserts the slow path
// skips domainPreFilter/sourceSemanticFilter entirely — no fake response is
// configured for question_domain_match.md or source_filter.md, so the test
// would fail with "no response configured" if either were called.
func TestSourceAffinityShortcut_HitSkipsDomainAndSourceFilter(t *testing.T) {
	svc, fake, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true

	subjectNorm := text.Normalize("linear equations")
	if err := store.RecordSourceAffinitySuccess("d1", subjectNorm, "s1"); err != nil {
		t.Fatal(err)
	}

	fake.SetResponse("outline_filter.md", llm.FakeResponse{
		Output: `{"node_ids": ["o2"]}`,
	})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`,
	})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "matches"}]}`,
	})

	qc := QueryContext{Question: "linear equations", Subject: "linear equations", DomainResolved: true, DomainIDs: []string{"d1"}}
	es, err := svc.retrieveSlowPath(context.Background(), qc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(es.DirectEvidence) == 0 {
		t.Fatalf("expected direct evidence via affinity shortcut, got none: %+v", es)
	}
}

// TestSourceAffinityShortcut_FailureEvictsBinding asserts a shortcut hit
// whose recall comes back empty records a circuit-breaker failure, and that
// enough consecutive failures delete the binding outright (no Beta score to
// decay — see migration 068's design comment).
func TestSourceAffinityShortcut_FailureEvictsBinding(t *testing.T) {
	svc, fake, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true
	svc.cfg.Retrieval.SourceAffinityFailureMax = 2

	subjectNorm := text.Normalize("purple dinosaur spacecraft manual")
	if err := store.RecordSourceAffinitySuccess("d1", subjectNorm, "s1"); err != nil {
		t.Fatal(err)
	}

	// No outline/rerank fake responses configured — outline FTS won't find
	// anything for this nonsense subject and the LLM fallback will error,
	// which outlineRecall treats as "no ids from that path", leaving
	// candidates empty and evidence empty.
	fake.SetResponse("outline_filter.md", llm.FakeResponse{
		Output: `{"node_ids": []}`,
	})

	qc := QueryContext{Question: "purple dinosaur spacecraft manual", Subject: "purple dinosaur spacecraft manual", DomainResolved: true, DomainIDs: []string{"d1"}}

	// First call: shortcut attempted, empty evidence, falls through to the
	// full pipeline (which also finds nothing, since nothing here actually
	// answers this question) — one consecutive_failures increment.
	if _, err := svc.retrieveSlowPath(context.Background(), qc, nil); err != nil {
		t.Fatal(err)
	}
	sources, err := store.GetSourceAffinitySources([]string{"d1"}, subjectNorm)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected binding to survive 1 failure (max=2), got %v", sources)
	}

	// Second call: another failure reaches the max and evicts the row.
	if _, err := svc.retrieveSlowPath(context.Background(), qc, nil); err != nil {
		t.Fatal(err)
	}
	sources, err = store.GetSourceAffinitySources([]string{"d1"}, subjectNorm)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("expected binding evicted after reaching failure max, got %v", sources)
	}
}
