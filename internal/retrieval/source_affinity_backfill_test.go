package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// TestBackfillSourceAffinityForSource_MatchesTagsWritesBackfillRows seeds two
// existing subject_norms tags for d1, configures source_tag_match.md to
// match only one of them against s1, and asserts exactly that one gets a
// source_affinity row while the other tag stays unbound.
func TestBackfillSourceAffinityForSource_MatchesTagsWritesBackfillRows(t *testing.T) {
	svc, fake, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true

	if err := store.InsertSubjectNorm(&SubjectNorm{DomainID: "d1", Subject: "linear equations"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSubjectNorm(&SubjectNorm{DomainID: "d1", Subject: "quadratic equations"}); err != nil {
		t.Fatal(err)
	}

	norms, err := store.ListAllSubjectNorms([]string{"d1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(norms) != 2 {
		t.Fatalf("expected 2 subject_norms rows seeded, got %d", len(norms))
	}
	var linearNormID string
	for _, n := range norms {
		if n.Subject == "linear equations" {
			linearNormID = n.NormID
		}
	}
	if linearNormID == "" {
		t.Fatalf("expected to find inserted 'linear equations' norm among %+v", norms)
	}

	fake.SetResponse("source_tag_match.md", llm.FakeResponse{
		Output: `{"relevant_ids": ["` + linearNormID + `"]}`,
	})

	if err := svc.BackfillSourceAffinityForSource(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	sources, err := store.GetSourceAffinitySources([]string{"d1"}, "linear equations")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0] != "s1" {
		t.Fatalf("expected backfill to bind s1 to 'linear equations', got %v", sources)
	}

	quadSources, err := store.GetSourceAffinitySources([]string{"d1"}, "quadratic equations")
	if err != nil {
		t.Fatal(err)
	}
	if len(quadSources) != 0 {
		t.Fatalf("expected no binding for the unmatched tag, got %v", quadSources)
	}
}

// TestBackfillSourceAffinityForSource_UnclassifiedSourceNoop asserts a source
// with no domain_id (s3 in the shared fixture) is a no-op — in particular it
// must never reach the LLM call, since no source_tag_match.md fake response
// is configured here and CompleteJSON would error/fail the test otherwise.
func TestBackfillSourceAffinityForSource_UnclassifiedSourceNoop(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true

	if err := svc.BackfillSourceAffinityForSource(context.Background(), "s3"); err != nil {
		t.Fatal(err)
	}
}

// TestBackfillSourceAffinityForSource_EmptyVocabularyNoop asserts a domain
// with zero subject_norms rows (the fresh fixture's default state) is a
// no-op, same LLM-call-avoidance reasoning as above.
func TestBackfillSourceAffinityForSource_EmptyVocabularyNoop(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true

	if err := svc.BackfillSourceAffinityForSource(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

// TestBackfillSourceAffinityForSource_DisabledConfigNoop asserts the whole
// step is a no-op when source_affinity_enabled is false, even with a
// matching tag vocabulary present.
func TestBackfillSourceAffinityForSource_DisabledConfigNoop(t *testing.T) {
	svc, _, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = false

	if err := store.InsertSubjectNorm(&SubjectNorm{DomainID: "d1", Subject: "linear equations"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.BackfillSourceAffinityForSource(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	sources, err := store.GetSourceAffinitySources([]string{"d1"}, "linear equations")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("expected no binding written while source_affinity disabled, got %v", sources)
	}
}
