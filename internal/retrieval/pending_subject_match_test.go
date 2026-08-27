package retrieval

import (
	"context"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// TestEnqueuePendingSubjectMatch_DedupsRepeatedEnqueue asserts repeated
// enqueues of the same (domain, subject) before it's processed collapse
// into one row, not one per ask.
func TestEnqueuePendingSubjectMatch_DedupsRepeatedEnqueue(t *testing.T) {
	_, _, store := setupTestService(t)

	if err := store.EnqueuePendingSubjectMatch("d1", "linear equations"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueuePendingSubjectMatch("d1", "linear equations"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueuePendingSubjectMatch("d1", "linear equations"); err != nil {
		t.Fatal(err)
	}

	pending, err := store.ListPendingSubjectMatches(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected exactly 1 pending row after 3 repeat enqueues, got %d: %+v", len(pending), pending)
	}
}

// TestProcessPendingSubjectMatches_MatchesAndClearsQueue enqueues a subject
// for d1 (which has sources s1 and s3 available — s2 belongs to d2 and must
// not be considered), configures source_filter.md to match only s1, and
// asserts: s1 gets bound, s3 does not, and the pending row is cleared
// regardless.
func TestProcessPendingSubjectMatches_MatchesAndClearsQueue(t *testing.T) {
	svc, fake, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = true

	if err := store.EnqueuePendingSubjectMatch("d1", "linear equations"); err != nil {
		t.Fatal(err)
	}

	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"relevant_ids": ["s1"]}`,
	})

	processed, err := svc.ProcessPendingSubjectMatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 pending entry processed, got %d", processed)
	}

	sources, err := store.GetSourceAffinitySources([]string{"d1"}, "linear equations")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0] != "s1" {
		t.Fatalf("expected s1 bound to 'linear equations', got %v", sources)
	}

	pending, err := store.ListPendingSubjectMatches(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pending queue cleared after processing, got %+v", pending)
	}
}

// TestProcessPendingSubjectMatches_DisabledConfigNoop asserts the whole step
// is a no-op when source_affinity_enabled is false — in particular it must
// never reach the LLM (no source_filter.md fake response configured here),
// and the pending row must survive untouched for whenever the feature is
// re-enabled.
func TestProcessPendingSubjectMatches_DisabledConfigNoop(t *testing.T) {
	svc, _, store := setupTestService(t)
	svc.cfg.Retrieval.SourceAffinityEnabled = false

	if err := store.EnqueuePendingSubjectMatch("d1", "linear equations"); err != nil {
		t.Fatal(err)
	}

	processed, err := svc.ProcessPendingSubjectMatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("expected 0 processed while disabled, got %d", processed)
	}

	pending, err := store.ListPendingSubjectMatches(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected the pending row to survive untouched while disabled, got %+v", pending)
	}
}
