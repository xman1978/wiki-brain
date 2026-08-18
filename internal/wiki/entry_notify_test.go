package wiki

import (
	"testing"
)

// TestNotifyEntriesChanged_PublishedPageFlagged covers docs/impl/v1/wiki.md
// 「重编译标记」a/新增 entry_id 归属变化两条来源共用的落地方法: a published
// page compiled from a changed entry_id gets flagged needs_recompile.
func TestNotifyEntriesChanged_PublishedPageFlagged(t *testing.T) {
	svc, _, db, wikiIdx := setupTestService(t)
	seedDomain(t, db, "d-2", "领域2")
	seedEntry(t, db, "c-pub", "d-2", "已发布概念")

	page := &Page{
		PageID: "page-pub", PageType: "entry", EntryID: nullStr("c-pub"),
		Title: "已发布概念", Content: "内容", Status: StatusPublished,
	}
	if err := svc.store.InsertPageWithEntries(page, []string{"c-pub"}); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	if err := svc.store.UpdatePageStatus(page.PageID, StatusPublished); err != nil {
		t.Fatalf("set published: %v", err)
	}
	if err := wikiIdx.Index(page.PageID, map[string]interface{}{"title": page.Title}); err != nil {
		t.Fatalf("seed bleve doc: %v", err)
	}

	if err := svc.NotifyEntriesChanged([]string{"c-pub"}, "test lifecycle change"); err != nil {
		t.Fatalf("NotifyEntriesChanged: %v", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if got.Status != StatusNeedsRecompile {
		t.Errorf("status = %q, want %q", got.Status, StatusNeedsRecompile)
	}
}

// TestNotifyEntriesChanged_DraftPageNotFlagged: a page that was never
// published has no readers to protect from staleness — left alone.
func TestNotifyEntriesChanged_DraftPageNotFlagged(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedDomain(t, db, "d-3", "领域3")
	seedEntry(t, db, "c-draft", "d-3", "草稿概念")

	page := &Page{
		PageID: "page-draft", PageType: "entry", EntryID: nullStr("c-draft"),
		Title: "草稿概念", Content: "内容", Status: StatusDraft,
	}
	if err := svc.store.InsertPageWithEntries(page, []string{"c-draft"}); err != nil {
		t.Fatalf("insert page: %v", err)
	}

	if err := svc.NotifyEntriesChanged([]string{"c-draft"}, "test lifecycle change"); err != nil {
		t.Fatalf("NotifyEntriesChanged: %v", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want unchanged %q", got.Status, StatusDraft)
	}
}

// TestNotifyEntriesChanged_ArchivedPageNotFlagged mirrors the draft case for
// a terminal-status page.
func TestNotifyEntriesChanged_ArchivedPageNotFlagged(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedDomain(t, db, "d-4", "领域4")
	seedEntry(t, db, "c-archived", "d-4", "已归档概念")

	page := &Page{
		PageID: "page-archived", PageType: "entry", EntryID: nullStr("c-archived"),
		Title: "已归档概念", Content: "内容", Status: StatusArchived,
	}
	if err := svc.store.InsertPageWithEntries(page, []string{"c-archived"}); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	if err := svc.store.UpdatePageStatus(page.PageID, StatusArchived); err != nil {
		t.Fatalf("set archived: %v", err)
	}

	if err := svc.NotifyEntriesChanged([]string{"c-archived"}, "test lifecycle change"); err != nil {
		t.Fatalf("NotifyEntriesChanged: %v", err)
	}

	got, err := svc.store.GetPage(page.PageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if got.Status != StatusArchived {
		t.Errorf("status = %q, want unchanged %q", got.Status, StatusArchived)
	}
}

// TestNotifyEntriesChanged_UncoveredEntryNoop: an entry_id no published page
// was ever compiled from is a pure no-op — no page, nothing to look up.
func TestNotifyEntriesChanged_UncoveredEntryNoop(t *testing.T) {
	svc, _, db, _ := setupTestService(t)
	seedDomain(t, db, "d-5", "领域5")
	seedEntry(t, db, "c-uncovered", "d-5", "无页面概念")

	if err := svc.NotifyEntriesChanged([]string{"c-uncovered"}, "test lifecycle change"); err != nil {
		t.Fatalf("NotifyEntriesChanged: %v", err)
	}
}
