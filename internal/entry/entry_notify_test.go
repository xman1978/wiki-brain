package entry

import (
	"database/sql"
	"testing"
)

// Covers docs/impl/v1/wiki.md「重编译标记」新增 entry_id 归属变化来源
// (2026-08-18 单层化收尾重新接线): manual entry_id membership edits
// (confirmAdd's "归入已有概念" ConfirmAssign branch, AddEntryPoints,
// RemoveEntryPoint) flag the target entry's published page needs_recompile.

func TestConfirmAdd_AssignToExisting_FlagsPublishedPage(t *testing.T) {
	svc, store, db, wikiSvc := setupServiceWithWiki(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	insertPublishedWikiPage(t, db, "page-1", "c1")

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "候选", EntryKindFact,
		[]string{"p1"}, nil, nil, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "c1"}, nil); err != nil {
		t.Fatalf("confirm add (assign): %v", err)
	}

	page, err := wikiSvc.GetActivePageByEntryID("c1")
	if err != nil {
		t.Fatalf("get active page: %v", err)
	}
	if page == nil || page.Status != "needs_recompile" {
		t.Errorf("page-1 not flagged needs_recompile: %+v", page)
	}
}

func TestConfirmAdd_AssignToExisting_DraftPageNotFlagged(t *testing.T) {
	svc, store, db, wikiSvc := setupServiceWithWiki(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	insertWikiPage(t, db, "page-1", "c1") // status=draft

	candidateID, err := store.InsertAddCandidate(sql.NullString{String: "d1", Valid: true}, "候选", EntryKindFact,
		[]string{"p1"}, nil, nil, "seed", sql.NullString{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Confirm(candidateID, &ConfirmAddRequest{EntryID: "c1"}, nil); err != nil {
		t.Fatalf("confirm add (assign): %v", err)
	}

	page, err := wikiSvc.GetActivePageByEntryID("c1")
	if err != nil {
		t.Fatalf("get active page: %v", err)
	}
	if page == nil || page.Status != "draft" {
		t.Errorf("draft page-1 should stay draft, got: %+v", page)
	}
}

func TestAddEntryPoints_FlagsPublishedPage(t *testing.T) {
	svc, _, db, wikiSvc := setupServiceWithWiki(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{})
	seedKP(t, db, "p1", "u1", "s1")
	insertPublishedWikiPage(t, db, "page-1", "c1")

	migrated, err := svc.AddEntryPoints("c1", []string{"p1"})
	if err != nil {
		t.Fatalf("add entry points: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("expected 1 migrated unit, got %d", migrated)
	}

	page, err := wikiSvc.GetActivePageByEntryID("c1")
	if err != nil {
		t.Fatalf("get active page: %v", err)
	}
	if page == nil || page.Status != "needs_recompile" {
		t.Errorf("page-1 not flagged needs_recompile: %+v", page)
	}
}

func TestRemoveEntryPoint_FlagsPublishedPage(t *testing.T) {
	svc, _, db, wikiSvc := setupServiceWithWiki(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")
	insertPublishedWikiPage(t, db, "page-1", "c1")

	if _, err := svc.RemoveEntryPoint("c1", "p1"); err != nil {
		t.Fatalf("remove entry point: %v", err)
	}

	page, err := wikiSvc.GetActivePageByEntryID("c1")
	if err != nil {
		t.Fatalf("get active page: %v", err)
	}
	if page == nil || page.Status != "needs_recompile" {
		t.Errorf("page-1 not flagged needs_recompile: %+v", page)
	}
}

func TestRemoveEntryPoint_UncoveredEntryNoop(t *testing.T) {
	svc, _, db, _ := setupServiceWithWiki(t)
	seedEntry(t, db, "c1", "d1", sql.NullString{})
	seedSource(t, db, "s1", "d1")
	seedKU(t, db, "u1", "s1", "topic", sql.NullString{String: "c1", Valid: true})
	seedKP(t, db, "p1", "u1", "s1")
	// No wiki page compiled from c1 at all — must not error.

	if _, err := svc.RemoveEntryPoint("c1", "p1"); err != nil {
		t.Fatalf("remove entry point: %v", err)
	}
}
