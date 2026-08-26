package activation

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// insertTestSynonym seeds a subject_synonyms row directly via SQL, mirroring
// how preset.go and manual entries write this table now that the gap-mining
// write path (Service.CreateSynonymCandidate/CreateActiveSynonym) has been
// removed (2026-08-26: TupleNormalizer's query-time canonicalization made the
// subject_synonym_gap discovery pipeline unreachable in practice, see
// docs/impl/v1/activation.md).
func insertTestSynonym(t *testing.T, db *sql.DB, term, canonical, status string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := db.Exec(`INSERT INTO subject_synonyms
		(synonym_id, term, canonical, source, status) VALUES (?, ?, ?, ?, ?)`,
		id, term, canonical, SynonymSourceManual, status); err != nil {
		t.Fatalf("insert test synonym: %v", err)
	}
	return id
}

func TestStore_Synonym_UpdateStatus(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	id := insertTestSynonym(t, db, "term-x", "canon-x", SynonymStatusCandidate)
	if err := store.UpdateSynonymStatus(id, SynonymStatusActive); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, err := store.GetSynonym(id)
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

	id1 := insertTestSynonym(t, db, "term-a", "canon-a", SynonymStatusCandidate)

	confirmed, err := svc.ConfirmSynonym(id1)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != SynonymStatusActive {
		t.Errorf("status = %q, want active", confirmed.Status)
	}

	// Re-confirm must fail — only candidate rows are confirmable.
	if _, err := svc.ConfirmSynonym(id1); err == nil {
		t.Error("expected re-confirm to fail")
	}

	id2 := insertTestSynonym(t, db, "term-b", "canon-b", SynonymStatusCandidate)
	rejected, err := svc.RejectSynonym(id2)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != SynonymStatusRejected {
		t.Errorf("status = %q, want rejected", rejected.Status)
	}
}

// TestService_RejectSynonym_ActiveRow: 2026-08-12 — reject must also accept
// status=active, not just candidate, since manually-confirmed rows (or
// preset-imported ones) are active from the start.
func TestService_RejectSynonym_ActiveRow(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	matcher := NewMatcher(store)
	svc := newTestService(store, matcher)

	id := insertTestSynonym(t, db, "term-c", "canon-c", SynonymStatusActive)

	rejected, err := svc.RejectSynonym(id)
	if err != nil {
		t.Fatalf("reject active row: %v", err)
	}
	if rejected.Status != SynonymStatusRejected {
		t.Errorf("status = %q, want rejected", rejected.Status)
	}

	// Rejected rows are terminal — a second reject must fail, not silently
	// succeed (no auto-revival).
	if _, err := svc.RejectSynonym(id); err == nil {
		t.Error("expected reject on an already-rejected row to fail")
	}
}
