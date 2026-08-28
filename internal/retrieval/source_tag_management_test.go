package retrieval

import (
	"context"
	"errors"
	"testing"
)

// TestListAndDeleteSourceAffinityBySourceID covers the source-detail page's
// basic read/delete path: seed two bindings for s1 (one for d1, one that
// would belong to a different domain if s1 had one — here just two distinct
// subjects under d1 since seedTestData only gives s1 a single domain), list
// them back, delete one by affinity_id, confirm only the other remains.
func TestListAndDeleteSourceAffinityBySourceID(t *testing.T) {
	_, _, store := setupTestService(t)

	if err := store.RecordSourceAffinitySuccess("d1", "linear equations", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSourceAffinitySuccess("d1", "quadratic equations", "s1"); err != nil {
		t.Fatal(err)
	}

	bindings, err := store.ListSourceAffinityBySourceID("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings for s1, got %d: %+v", len(bindings), bindings)
	}

	var toDelete string
	for _, b := range bindings {
		if b.SubjectNorm == "linear equations" {
			toDelete = b.AffinityID
		}
	}
	if toDelete == "" {
		t.Fatalf("expected to find the 'linear equations' binding among %+v", bindings)
	}

	if err := store.DeleteSourceAffinityByID(toDelete); err != nil {
		t.Fatal(err)
	}

	bindings, err = store.ListSourceAffinityBySourceID("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].SubjectNorm != "quadratic equations" {
		t.Fatalf("expected only 'quadratic equations' left, got %+v", bindings)
	}
}

// TestAddSourceSubjectTag_NoDomainErrors asserts a source with no domain_id
// (s3 in the shared fixture) can't have a tag added — subject_norms/
// source_affinity are domain-scoped by design.
func TestAddSourceSubjectTag_NoDomainErrors(t *testing.T) {
	svc, _, _ := setupTestService(t)

	_, err := svc.AddSourceSubjectTag(context.Background(), "s3", "some topic")
	if !errors.Is(err, ErrSourceHasNoDomain) {
		t.Fatalf("expected ErrSourceHasNoDomain, got %v", err)
	}
}

// TestAddSourceSubjectTag_NormalizesAndBinds asserts adding a tag goes
// through SubjectNormalizer (Tier1 exact match against an existing subject
// norm here, no LLM needed) rather than writing the raw text straight into
// subject_norm, and that the resulting binding is visible via
// ListSourceSubjectTags.
func TestAddSourceSubjectTag_NormalizesAndBinds(t *testing.T) {
	svc, _, store := setupTestService(t)

	if err := store.InsertSubjectNorm(&SubjectNorm{DomainID: "d1", Subject: "linear equations"}); err != nil {
		t.Fatal(err)
	}

	binding, err := svc.AddSourceSubjectTag(context.Background(), "s1", "linear equations")
	if err != nil {
		t.Fatal(err)
	}
	if binding.SubjectNorm != "linear equations" || binding.DomainID != "d1" {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if binding.AffinityID == "" {
		t.Fatalf("expected affinity_id to be populated, got %+v", binding)
	}

	tags, err := svc.ListSourceSubjectTags("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].SubjectNorm != "linear equations" {
		t.Fatalf("expected 1 tag 'linear equations', got %+v", tags)
	}
}

// TestRemoveSourceSubjectTag_ByAffinityID mirrors the delete flow through
// the Service-level wrapper the handler actually calls.
func TestRemoveSourceSubjectTag_ByAffinityID(t *testing.T) {
	svc, _, store := setupTestService(t)

	if err := store.RecordSourceAffinitySuccess("d1", "linear equations", "s1"); err != nil {
		t.Fatal(err)
	}
	bindings, err := store.ListSourceAffinityBySourceID("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 seeded binding, got %+v", bindings)
	}

	if err := svc.RemoveSourceSubjectTag(bindings[0].AffinityID); err != nil {
		t.Fatal(err)
	}

	bindings, err = store.ListSourceAffinityBySourceID("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Fatalf("expected binding removed, got %+v", bindings)
	}
}
