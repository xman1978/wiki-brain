package wiki

import (
	"context"
	"encoding/json"
	"testing"
)

// TestUpdateDraft_SyncsToPage covers the 2026-08-19 write-back feature: every
// PATCH /wiki/drafts/:id save (Service.UpdateDraft) that changes title or
// content must write straight through to the source page via
// SyncDraftToPage — no LLM, no citation whitelist, the human's edit taken
// as-is, with a new revision recorded and the page reset to draft.
func TestUpdateDraft_SyncsToPage(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryIDs: []string{"c1"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := svc.Publish(page.PageID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	draft, err := svc.CreateDraft(page.PageID, DraftModePage)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	newTitle := "人工改写后的标题"
	newContent := "## 摘要\n\n人工改写的摘要。[p1]\n\n## 稳定结论\n\n人工改写的结论。[p1]\n"
	if _, err := svc.UpdateDraft(draft.DraftID, &newTitle, &newContent, nil); err != nil {
		t.Fatalf("update draft: %v", err)
	}

	updated, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("page title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Content != newContent {
		t.Errorf("page content = %q, want %q", updated.Content, newContent)
	}
	if updated.Status != StatusDraft {
		t.Errorf("page status = %q, want draft (edited content must clear the quality gate again)", updated.Status)
	}
	if updated.Summary != "人工改写的摘要。[p1]" {
		t.Errorf("page summary = %q, want re-extracted from edited content", updated.Summary)
	}

	var sourcePointIDs []string
	if err := json.Unmarshal([]byte(updated.SourcePointIDs), &sourcePointIDs); err != nil {
		t.Fatalf("unmarshal source_point_ids: %v", err)
	}
	if len(sourcePointIDs) != 1 || sourcePointIDs[0] != "p1" {
		t.Errorf("source_point_ids = %v, want [p1] (re-derived from edited content's [p1] tag)", sourcePointIDs)
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 3 || revs[len(revs)-1].Reason != "draft_sync" {
		t.Errorf("expected [compile, publish, draft_sync] revisions, got %v", revs)
	}
	if revs[len(revs)-1].Title != newTitle {
		t.Errorf("latest revision title = %q, want %q", revs[len(revs)-1].Title, newTitle)
	}

	// The draft that just wrote this revision shouldn't show as stale against
	// the very revision it produced.
	withStale, err := svc.GetDraftWithStale(draft.DraftID)
	if err != nil {
		t.Fatalf("get draft with stale: %v", err)
	}
	if withStale.Stale {
		t.Error("draft should not be marked stale immediately after its own save produced the latest revision")
	}
}

// TestUpdateDraft_NoteOnlyDoesNotSync covers the guard that a note-only edit
// (title and content both nil) must not touch the page at all — syncing on
// every PATCH regardless of what changed would blow away independent
// edits/timing for no reason.
func TestUpdateDraft_NoteOnlyDoesNotSync(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryIDs: []string{"c1"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	draft, err := svc.CreateDraft(page.PageID, DraftModePage)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	before, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}

	note := "仅备注"
	if _, err := svc.UpdateDraft(draft.DraftID, nil, nil, &note); err != nil {
		t.Fatalf("update draft: %v", err)
	}

	after, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("page updated_at changed on a note-only draft edit: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("expected no new revision from a note-only edit, got %d", len(revs))
	}
}

// TestDeleteDraft_RemovesDraftSyncRevision covers the merged draft/revision
// deletion: deleting a draft that has been saved (and so wrote its own
// draft_sync revision) must remove that revision too, not just the draft
// row — otherwise the revision list keeps an orphaned entry no draft editor
// can open anymore.
func TestDeleteDraft_RemovesDraftSyncRevision(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryIDs: []string{"c1"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	draft, err := svc.CreateDraft(page.PageID, DraftModePage)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	// Save twice — SyncDraftToPage inserts a NEW revision on every save, so
	// this draft has now written two draft_sync revisions, but
	// wiki_drafts.source_revision_id only tracks the latest one.
	newTitle := "人工改写后的标题"
	newContent := "## 摘要\n\n人工改写的摘要。[p1]\n\n## 稳定结论\n\n人工改写的结论。[p1]\n"
	if _, err := svc.UpdateDraft(draft.DraftID, &newTitle, &newContent, nil); err != nil {
		t.Fatalf("update draft: %v", err)
	}
	newerContent := "## 摘要\n\n再次改写的摘要。[p1]\n\n## 稳定结论\n\n再次改写的结论。[p1]\n"
	if _, err := svc.UpdateDraft(draft.DraftID, nil, &newerContent, nil); err != nil {
		t.Fatalf("update draft again: %v", err)
	}

	if err := svc.DeleteDraft(draft.DraftID); err != nil {
		t.Fatalf("delete draft: %v", err)
	}

	if d, err := svc.store.GetDraft(draft.DraftID); err != nil {
		t.Fatalf("get draft: %v", err)
	} else if d != nil {
		t.Error("draft should be deleted")
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("expected only the original compile revision to remain (both draft_sync revisions from the two saves deleted), got %v", revs)
	}
	for _, r := range revs {
		if r.Reason == "draft_sync" {
			t.Errorf("draft_sync revision should have been deleted along with the draft, got %v", revs)
		}
	}
}

// TestDeleteDraft_NeverSaved_KeepsOriginalRevision covers the guard that a
// draft deleted before ever being saved must NOT delete the revision it was
// derived from (CreateDraft's source_revision_id points at the original
// compile revision, not something this draft produced).
func TestDeleteDraft_NeverSaved_KeepsOriginalRevision(t *testing.T) {
	svc, _, _, _ := setupTestService(t)

	page, err := svc.Compile(context.Background(), CompileRequest{EntryIDs: []string{"c1"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	draft, err := svc.CreateDraft(page.PageID, DraftModePage)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	if err := svc.DeleteDraft(draft.DraftID); err != nil {
		t.Fatalf("delete draft: %v", err)
	}

	revs, err := svc.store.ListRevisions(page.PageID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("original compile revision should remain untouched, got %v", revs)
	}
}
