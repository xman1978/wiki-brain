package activation

import (
	"testing"
)

func TestSynonymResolver_Canonicalize_LongestFirst(t *testing.T) {
	r := NewSynonymResolver()
	r.Load([]SubjectSynonym{
		{Term: "股票", Canonical: "证券", Status: SynonymStatusActive},
		{Term: "股票市场", Canonical: "证券市场", Status: SynonymStatusActive},
	})

	// The longer term ("股票市场"->"证券市场") must be substituted before the
	// shorter one ("股票"->"证券") would otherwise partially consume it and
	// produce a mixed result like "证券市场" via two separate substitutions
	// landing on the wrong composition.
	got := r.Canonicalize("股票市场怎么运作")
	want := "证券市场怎么运作"
	if got != want {
		t.Errorf("Canonicalize = %q, want %q", got, want)
	}
}

func TestSynonymResolver_Canonicalize_NoMatch_ReturnsUnchanged(t *testing.T) {
	r := NewSynonymResolver()
	r.Load([]SubjectSynonym{
		{Term: "招待费报销", Canonical: "差旅报销", Status: SynonymStatusActive},
	})

	got := r.Canonicalize("住宿标准")
	if got != "住宿标准" {
		t.Errorf("Canonicalize = %q, want unchanged 住宿标准", got)
	}
}

func TestSynonymResolver_Canonicalize_IgnoresNonActiveRows(t *testing.T) {
	r := NewSynonymResolver()
	r.Load([]SubjectSynonym{
		{Term: "证券市场", Canonical: "股票市场", Status: SynonymStatusCandidate},
		{Term: "二级市场", Canonical: "股票市场", Status: SynonymStatusRejected},
	})

	if got := r.Canonicalize("证券市场"); got != "证券市场" {
		t.Errorf("candidate row must not apply, got %q", got)
	}
	if got := r.Canonicalize("二级市场"); got != "二级市场" {
		t.Errorf("rejected row must not apply, got %q", got)
	}
}

func TestSynonymResolver_Canonicalize_EmptyInput(t *testing.T) {
	r := NewSynonymResolver()
	r.Load([]SubjectSynonym{{Term: "a", Canonical: "b", Status: SynonymStatusActive}})
	if got := r.Canonicalize(""); got != "" {
		t.Errorf("Canonicalize(\"\") = %q, want empty", got)
	}
}

func TestStore_Synonym_InsertCandidateAndFindByTerm(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	syn, err := store.InsertSynonymCandidate("", "证券市场", "股票市场", []string{"evt-1"})
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	if syn.Status != SynonymStatusCandidate || syn.Source != SynonymSourceGapMined {
		t.Errorf("unexpected candidate row: %+v", syn)
	}

	found, err := store.FindSynonymByTermAnyStatus("证券市场")
	if err != nil {
		t.Fatalf("find by term: %v", err)
	}
	if found == nil || found.SynonymID != syn.SynonymID {
		t.Fatalf("expected to find the just-inserted candidate, got %+v", found)
	}

	missing, err := store.FindSynonymByTermAnyStatus("从未出现过的词")
	if err != nil {
		t.Fatalf("find by term (missing): %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for unregistered term, got %+v", missing)
	}
}

func TestStore_Synonym_ListActiveSynonyms_OnlyActive(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	if _, err := store.InsertSynonymCandidate("", "candidate-term", "canon", nil); err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	active, err := store.InsertActiveSynonym("", "active-term", "canon", nil)
	if err != nil {
		t.Fatalf("insert active: %v", err)
	}

	rows, err := store.ListActiveSynonyms()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(rows) != 1 || rows[0].SynonymID != active.SynonymID {
		t.Fatalf("expected only the active row, got %+v", rows)
	}
}

func TestStore_Synonym_UpdateStatus(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	syn, err := store.InsertSynonymCandidate("", "term-x", "canon-x", nil)
	if err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	if err := store.UpdateSynonymStatus(syn.SynonymID, SynonymStatusActive); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, err := store.GetSynonym(syn.SynonymID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != SynonymStatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestService_ConfirmRejectSynonym(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := newTestService(store, matcher)

	syn, err := svc.CreateSynonymCandidate("", "term-a", "canon-a", nil)
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}

	confirmed, err := svc.ConfirmSynonym(syn.SynonymID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != SynonymStatusActive {
		t.Errorf("status = %q, want active", confirmed.Status)
	}

	// Re-confirm must fail — only candidate rows are confirmable.
	if _, err := svc.ConfirmSynonym(syn.SynonymID); err == nil {
		t.Error("expected re-confirm to fail")
	}

	syn2, err := svc.CreateSynonymCandidate("", "term-b", "canon-b", nil)
	if err != nil {
		t.Fatalf("create candidate 2: %v", err)
	}
	rejected, err := svc.RejectSynonym(syn2.SynonymID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != SynonymStatusRejected {
		t.Errorf("status = %q, want rejected", rejected.Status)
	}
}

// TestService_RejectSynonym_ActiveRow: 2026-08-12 — reject must also accept
// status=active, not just candidate, since synonym_auto_promote now defaults
// true and most gap_mined rows land directly on active.
func TestService_RejectSynonym_ActiveRow(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := newTestService(store, matcher)

	syn, err := svc.CreateSynonymCandidate("", "term-c", "canon-c", nil)
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	active, err := svc.ConfirmSynonym(syn.SynonymID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if active.Status != SynonymStatusActive {
		t.Fatalf("status = %q, want active", active.Status)
	}

	rejected, err := svc.RejectSynonym(syn.SynonymID)
	if err != nil {
		t.Fatalf("reject active row: %v", err)
	}
	if rejected.Status != SynonymStatusRejected {
		t.Errorf("status = %q, want rejected", rejected.Status)
	}

	// Rejected rows are terminal — a second reject must fail, not silently
	// succeed (no auto-revival).
	if _, err := svc.RejectSynonym(syn.SynonymID); err == nil {
		t.Error("expected reject on an already-rejected row to fail")
	}
}
