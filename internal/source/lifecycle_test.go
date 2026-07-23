package source

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type fakeLifecycleSetter struct {
	calls []struct {
		unitIDs   []string
		lifecycle string
		reason    string
	}
	deprecateCalls [][]string
	restoreCalls   [][]string
	reindexCalls   []string
}

func (f *fakeLifecycleSetter) SetUnitLifecycle(unitIDs []string, lifecycle, reason string) error {
	f.calls = append(f.calls, struct {
		unitIDs   []string
		lifecycle string
		reason    string
	}{unitIDs, lifecycle, reason})
	return nil
}

func (f *fakeLifecycleSetter) SnapshotAndDeprecate(unitIDs []string, reason string) error {
	f.deprecateCalls = append(f.deprecateCalls, unitIDs)
	return f.SetUnitLifecycle(unitIDs, "deprecated", reason)
}

func (f *fakeLifecycleSetter) RestoreLifecycle(unitIDs []string, reason string) error {
	f.restoreCalls = append(f.restoreCalls, unitIDs)
	return f.SetUnitLifecycle(unitIDs, "current", reason)
}

func (f *fakeLifecycleSetter) ReindexSource(sourceID string) error {
	f.reindexCalls = append(f.reindexCalls, sourceID)
	return nil
}

func insertUnitForSource(t *testing.T, svc *Service, sourceID, unitID string) {
	t.Helper()
	_, err := svc.store.db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, 'test center', 1, 5, 'completed', 'v1')`, unitID, sourceID)
	if err != nil {
		t.Fatalf("insert unit: %v", err)
	}
}

func TestSoftDelete_MarksUnitsDeprecatedAndStatus(t *testing.T) {
	svc, _ := setupTestService(t)
	lc := &fakeLifecycleSetter{}
	svc.SetLifecycleSetter(lc)

	svc.store.Create(&Source{
		SourceID: "sd-1", Title: "Test", Format: "markdown", FileName: "sd.md",
		OriginalPath: "o/sd.md", MarkdownPath: "m/sd.md", Status: "completed",
	})
	insertUnitForSource(t, svc, "sd-1", "u1")
	insertUnitForSource(t, svc, "sd-1", "u2")

	n, err := svc.SoftDelete("sd-1")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if n != 2 {
		t.Errorf("deprecated count = %d, want 2", n)
	}

	if len(lc.calls) != 1 {
		t.Fatalf("expected 1 SetUnitLifecycle call, got %d", len(lc.calls))
	}
	if lc.calls[0].lifecycle != "deprecated" {
		t.Errorf("lifecycle = %q, want deprecated", lc.calls[0].lifecycle)
	}

	got, err := svc.store.GetByID("sd-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "deleted" {
		t.Errorf("status = %q, want deleted", got.Status)
	}

	if len(lc.deprecateCalls) != 1 {
		t.Errorf("expected 1 SnapshotAndDeprecate call, got %d", len(lc.deprecateCalls))
	}
}

// TestRestore_FlipsStatusBackAndCallsLifecycleRestore covers 文件管理 恢复按钮:
// Restore is SoftDelete's reverse — status flips back to completed and the
// lifecycle restore path (not a blind SetUnitLifecycle) is invoked.
func TestRestore_FlipsStatusBackAndCallsLifecycleRestore(t *testing.T) {
	svc, _ := setupTestService(t)
	lc := &fakeLifecycleSetter{}
	svc.SetLifecycleSetter(lc)

	svc.store.Create(&Source{
		SourceID: "rs-1", Title: "Test", Format: "markdown", FileName: "rs.md",
		OriginalPath: "o/rs.md", MarkdownPath: "m/rs.md", Status: "completed",
	})
	insertUnitForSource(t, svc, "rs-1", "u1")
	insertUnitForSource(t, svc, "rs-1", "u2")

	if _, err := svc.SoftDelete("rs-1"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	n, err := svc.Restore("rs-1")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if n != 2 {
		t.Errorf("restored count = %d, want 2", n)
	}
	if len(lc.restoreCalls) != 1 {
		t.Fatalf("expected 1 RestoreLifecycle call, got %d", len(lc.restoreCalls))
	}

	got, err := svc.store.GetByID("rs-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
}

// TestRestore_RejectsNonDeletedSource covers the guard: only a soft-deleted
// source has anything to restore.
func TestRestore_RejectsNonDeletedSource(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.SetLifecycleSetter(&fakeLifecycleSetter{})

	svc.store.Create(&Source{
		SourceID: "rs-2", Title: "Test", Format: "markdown", FileName: "rs2.md",
		OriginalPath: "o/rs2.md", MarkdownPath: "m/rs2.md", Status: "completed",
	})

	if _, err := svc.Restore("rs-2"); err == nil {
		t.Error("expected error restoring a non-deleted source")
	}
}

func TestList_ExcludesShadowSources(t *testing.T) {
	svc, _ := setupTestService(t)

	svc.store.Create(&Source{
		SourceID: "vis-1", Title: "Visible", Format: "markdown", FileName: "vis.md",
		OriginalPath: "o/vis.md", MarkdownPath: "m/vis.md", Status: "completed",
	})
	svc.store.Create(&Source{
		SourceID: "shadow-1", Title: "Shadow", Format: "markdown", FileName: "vis.md",
		OriginalPath: "o/shadow.md", MarkdownPath: "m/shadow.md", Status: "completed",
		ShadowOf: sql.NullString{String: "vis-1", Valid: true},
	})

	list, err := svc.store.List("", "", 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, s := range list {
		if s.SourceID == "shadow-1" {
			t.Error("shadow source should not appear in List")
		}
	}

	count, err := svc.store.Count("", "")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (shadow excluded)", count)
	}
}

func TestExistsByFileNameExcept(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.store.Create(&Source{
		SourceID: "orig-1", Title: "Orig", Format: "markdown", FileName: "same.md",
		OriginalPath: "o/orig.md", MarkdownPath: "m/orig.md", Status: "completed",
	})

	// Excluding the source that owns the name: no collision.
	exists, err := svc.store.ExistsByFileNameExcept("same.md", "orig-1")
	if err != nil {
		t.Fatalf("ExistsByFileNameExcept: %v", err)
	}
	if exists {
		t.Error("expected no collision when excluding the file's own owner")
	}

	// A different source with the same name still collides.
	exists, err = svc.store.ExistsByFileNameExcept("same.md", "other-id")
	if err != nil {
		t.Fatalf("ExistsByFileNameExcept: %v", err)
	}
	if !exists {
		t.Error("expected collision against an unrelated source")
	}
}

func TestImportShadow_AllowsSameFileNameAsTarget(t *testing.T) {
	svc, fake := setupTestService(t)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := svc.Import(context.Background(), "report.md", strings.NewReader("# Report\n\nOld content"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	shadow, err := svc.ImportShadow(context.Background(), target.SourceID, "report.md", strings.NewReader("# Report\n\nNew content"))
	if err != nil {
		t.Fatalf("ImportShadow: %v", err)
	}
	if !shadow.ShadowOf.Valid || shadow.ShadowOf.String != target.SourceID {
		t.Errorf("shadow_of = %v, want %s", shadow.ShadowOf, target.SourceID)
	}
	if shadow.SourceID == target.SourceID {
		t.Error("shadow must get its own source_id")
	}
}

func TestImportShadow_RejectsUnrelatedDuplicateFileName(t *testing.T) {
	svc, fake := setupTestService(t)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := svc.Import(context.Background(), "a.md", strings.NewReader("# A"))
	if err != nil {
		t.Fatalf("Import target: %v", err)
	}
	if _, err := svc.Import(context.Background(), "b.md", strings.NewReader("# B")); err != nil {
		t.Fatalf("Import other: %v", err)
	}

	_, err = svc.ImportShadow(context.Background(), target.SourceID, "b.md", strings.NewReader("# New"))
	if err == nil || !strings.Contains(err.Error(), "duplicate file name") {
		t.Fatalf("expected duplicate file name error, got %v", err)
	}
}

func TestImportShadow_DiscardsStaleFailedShadow(t *testing.T) {
	svc, fake := setupTestService(t)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := svc.Import(context.Background(), "a.md", strings.NewReader("# A"))
	if err != nil {
		t.Fatalf("Import target: %v", err)
	}

	firstShadow, err := svc.ImportShadow(context.Background(), target.SourceID, "attempt1.md", strings.NewReader("# Attempt 1"))
	if err != nil {
		t.Fatalf("ImportShadow first: %v", err)
	}
	errMsg := "boom"
	if err := svc.store.UpdateStatus(firstShadow.SourceID, "failed", &errMsg); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	secondShadow, err := svc.ImportShadow(context.Background(), target.SourceID, "attempt2.md", strings.NewReader("# Attempt 2"))
	if err != nil {
		t.Fatalf("ImportShadow second: %v", err)
	}
	if secondShadow.SourceID == firstShadow.SourceID {
		t.Error("expected a fresh shadow row, not a reused one")
	}

	if _, err := svc.store.GetByID(firstShadow.SourceID); err == nil {
		t.Error("stale failed shadow should have been discarded")
	}

	got, err := svc.store.GetShadowByTarget(target.SourceID)
	if err != nil {
		t.Fatalf("GetShadowByTarget: %v", err)
	}
	if got == nil || got.SourceID != secondShadow.SourceID {
		t.Errorf("expected only the second shadow to remain, got %v", got)
	}
}

func TestReuploadRetry_RequiresFailedShadow(t *testing.T) {
	svc, fake := setupTestService(t)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := svc.Import(context.Background(), "a.md", strings.NewReader("# A"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// No shadow at all yet.
	if _, err := svc.ReuploadRetry(context.Background(), target.SourceID); err == nil {
		t.Error("expected error when no shadow exists")
	}
}

func TestSwapShadowIntoTarget_ReparentsAndCopiesMetadata(t *testing.T) {
	svc, _ := setupTestService(t)

	svc.store.Create(&Source{
		SourceID: "target-1", Title: "Target Title", Format: "markdown", FileName: "old.md",
		OriginalPath: "o/target-1.md", MarkdownPath: "m/target-1.md", Status: "completed",
		Summary: sql.NullString{String: "old summary", Valid: true},
	})
	svc.store.Create(&Source{
		SourceID: "shadow-1", Title: "Shadow Title", Format: "markdown", FileName: "new.md",
		OriginalPath: "o/shadow-1.md", MarkdownPath: "m/shadow-1.md", Status: "completed",
		Summary:   sql.NullString{String: "new summary", Valid: true},
		WordCount: sql.NullInt64{Int64: 42, Valid: true},
		ShadowOf:  sql.NullString{String: "target-1", Valid: true},
	})

	_, err := svc.store.db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('shadow-u1', 'shadow-1', 'c', 1, 5, 'completed', 'v1')`)
	if err != nil {
		t.Fatalf("insert shadow unit: %v", err)
	}

	if err := svc.store.SwapShadowIntoTarget("shadow-1", "target-1", "o/target-1-new.md", sql.NullString{}); err != nil {
		t.Fatalf("SwapShadowIntoTarget: %v", err)
	}

	got, err := svc.store.GetByID("target-1")
	if err != nil {
		t.Fatalf("GetByID target: %v", err)
	}
	if got.Title != "Target Title" {
		t.Errorf("title = %q, want unchanged Target Title", got.Title)
	}
	if got.FileName != "new.md" {
		t.Errorf("file_name = %q, want new.md (copied from shadow)", got.FileName)
	}
	if !got.Summary.Valid || got.Summary.String != "new summary" {
		t.Errorf("summary = %v, want new summary (copied from shadow)", got.Summary)
	}
	if got.OriginalPath != "o/target-1-new.md" {
		t.Errorf("original_path = %q, want o/target-1-new.md", got.OriginalPath)
	}

	if _, err := svc.store.GetByID("shadow-1"); err == nil {
		t.Error("shadow row should be deleted after swap")
	}

	var reparented string
	if err := svc.store.db.QueryRow(`SELECT source_id FROM knowledge_units WHERE unit_id = 'shadow-u1'`).Scan(&reparented); err != nil {
		t.Fatalf("query reparented unit: %v", err)
	}
	if reparented != "target-1" {
		t.Errorf("shadow unit source_id = %q, want target-1", reparented)
	}

	if got.Version != 2 {
		t.Errorf("version = %d, want 2 (incremented from the default 1)", got.Version)
	}
}

// TestCompleteShadowSwap_EmptyShadowLeavesTargetUntouched guards against a
// reupload with zero extracted units (e.g. an empty or unparseable file)
// silently wiping the target's real content: CompleteShadowSwap must refuse
// the swap and return ErrShadowEmpty instead of superseding the target's KUs.
func TestCompleteShadowSwap_EmptyShadowLeavesTargetUntouched(t *testing.T) {
	svc, _ := setupTestService(t)
	lc := &fakeLifecycleSetter{}
	svc.SetLifecycleSetter(lc)
	ctx := context.Background()

	svc.store.Create(&Source{
		SourceID: "target-2", Title: "T", Format: "markdown", FileName: "old.md",
		OriginalPath: "o/target-2.md", MarkdownPath: "m/target-2.md", Status: "completed",
	})
	insertUnitForSource(t, svc, "target-2", "target-2-u1")

	svc.store.Create(&Source{
		SourceID: "shadow-2", Title: "T", Format: "markdown", FileName: "new.md",
		OriginalPath: "o/shadow-2.md", MarkdownPath: "m/shadow-2.md",
		Status: "completed", ShadowOf: sql.NullString{String: "target-2", Valid: true},
	})
	// Deliberately no units inserted for shadow-2 — simulates extraction
	// producing zero KUs.

	if err := svc.CompleteShadowSwap(ctx, "shadow-2"); !errors.Is(err, ErrShadowEmpty) {
		t.Fatalf("CompleteShadowSwap error = %v, want ErrShadowEmpty", err)
	}

	if len(lc.calls) != 0 {
		t.Errorf("target's units must not be superseded, got lifecycle calls: %v", lc.calls)
	}
	if _, err := svc.store.GetByID("shadow-2"); err != nil {
		t.Errorf("shadow row should still exist (not swapped/deleted): %v", err)
	}
	var stillTarget string
	if err := svc.store.db.QueryRow(`SELECT source_id FROM knowledge_units WHERE unit_id = 'target-2-u1'`).Scan(&stillTarget); err != nil {
		t.Fatalf("query target unit: %v", err)
	}
	if stillTarget != "target-2" {
		t.Errorf("target's unit source_id = %q, want unchanged target-2", stillTarget)
	}
}

// TestCompleteShadowSwap_RecordsVersionSnapshot exercises the full
// CompleteShadowSwap (unlike the store-level test above) so
// archiveAndSwapFiles actually moves files on disk, verifying: target's
// version increments, and a source_versions row is recorded pointing at the
// archived (pre-reupload) file with the version number it superseded.
func TestCompleteShadowSwap_RecordsVersionSnapshot(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	writeFile := func(rel, content string) {
		full := filepath.Join(svc.baseDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	writeFile("data/sources/original/target-1.md", "旧版原文")
	writeFile("data/sources/markdown/target-1.md", "旧版原文")
	svc.store.Create(&Source{
		SourceID: "target-1", Title: "T", Format: "markdown", FileName: "old.md",
		OriginalPath: "data/sources/original/target-1.md", MarkdownPath: "data/sources/markdown/target-1.md",
		Status: "completed",
	})

	writeFile("data/sources/original/shadow-1.md", "新版原文")
	writeFile("data/sources/markdown/shadow-1.md", "新版原文")
	svc.store.Create(&Source{
		SourceID: "shadow-1", Title: "T", Format: "markdown", FileName: "new.md",
		OriginalPath: "data/sources/original/shadow-1.md", MarkdownPath: "data/sources/markdown/shadow-1.md",
		Status: "completed", ShadowOf: sql.NullString{String: "target-1", Valid: true},
	})
	insertUnitForSource(t, svc, "shadow-1", "shadow-u1")

	if err := svc.CompleteShadowSwap(ctx, "shadow-1"); err != nil {
		t.Fatalf("CompleteShadowSwap: %v", err)
	}

	target, err := svc.store.GetByID("target-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if target.Version != 2 {
		t.Errorf("version = %d, want 2", target.Version)
	}

	versions, err := svc.store.GetSourceVersions("target-1")
	if err != nil {
		t.Fatalf("GetSourceVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want 1", len(versions))
	}
	if versions[0].Version != 1 {
		t.Errorf("archived version number = %d, want 1 (the superseded version)", versions[0].Version)
	}
	if versions[0].FileName != "old.md" {
		t.Errorf("archived file_name = %q, want old.md", versions[0].FileName)
	}

	archivedContent, err := os.ReadFile(filepath.Join(svc.baseDir, versions[0].OriginalPath))
	if err != nil {
		t.Fatalf("read archived original: %v", err)
	}
	if string(archivedContent) != "旧版原文" {
		t.Errorf("archived original content = %q, want 旧版原文", archivedContent)
	}

	newContent, err := os.ReadFile(filepath.Join(svc.baseDir, target.MarkdownPath))
	if err != nil {
		t.Fatalf("read current markdown: %v", err)
	}
	if string(newContent) != "新版原文" {
		t.Errorf("current markdown = %q, want 新版原文 (swapped in from shadow)", newContent)
	}
}

// TestCompleteShadowSwap_SecondReuploadSkipsAlreadySupersededUnits is the
// regression test for the double-reupload bug: on a second reupload, the
// target already holds a unit superseded by the *first* reupload alongside
// its current unit. The swap must only pass the still-current unit to
// SetUnitLifecycle — re-touching the already-superseded one would overwrite
// its lifecycle_changed_at, destroying the record of which reupload actually
// superseded it.
func TestCompleteShadowSwap_SecondReuploadSkipsAlreadySupersededUnits(t *testing.T) {
	svc, _ := setupTestService(t)
	lc := &fakeLifecycleSetter{}
	svc.SetLifecycleSetter(lc)
	ctx := context.Background()

	writeFile := func(rel, content string) {
		full := filepath.Join(svc.baseDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	writeFile("data/sources/original/target-3.md", "v2 原文")
	writeFile("data/sources/markdown/target-3.md", "v2 原文")
	svc.store.Create(&Source{
		SourceID: "target-3", Title: "T", Format: "markdown", FileName: "old.md",
		OriginalPath: "data/sources/original/target-3.md", MarkdownPath: "data/sources/markdown/target-3.md",
		Status: "completed",
	})
	// v1, already superseded by an earlier reupload — its lifecycle_changed_at
	// must survive untouched.
	insertUnitForSource(t, svc, "target-3", "target-3-v1")
	if _, err := svc.store.db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded', lifecycle_changed_at = '2026-01-01 00:00:00' WHERE unit_id = 'target-3-v1'`); err != nil {
		t.Fatalf("seed superseded unit: %v", err)
	}
	// v2, the version this (second) reupload actually replaces.
	insertUnitForSource(t, svc, "target-3", "target-3-v2")

	writeFile("data/sources/original/shadow-3.md", "v3 原文")
	writeFile("data/sources/markdown/shadow-3.md", "v3 原文")
	svc.store.Create(&Source{
		SourceID: "shadow-3", Title: "T", Format: "markdown", FileName: "new.md",
		OriginalPath: "data/sources/original/shadow-3.md", MarkdownPath: "data/sources/markdown/shadow-3.md",
		Status: "completed", ShadowOf: sql.NullString{String: "target-3", Valid: true},
	})
	insertUnitForSource(t, svc, "shadow-3", "shadow-3-u1")

	if err := svc.CompleteShadowSwap(ctx, "shadow-3"); err != nil {
		t.Fatalf("CompleteShadowSwap: %v", err)
	}

	if len(lc.calls) != 1 {
		t.Fatalf("expected 1 SetUnitLifecycle call, got %d: %+v", len(lc.calls), lc.calls)
	}
	if lc.calls[0].lifecycle != "superseded" {
		t.Errorf("lifecycle = %q, want superseded", lc.calls[0].lifecycle)
	}
	if len(lc.calls[0].unitIDs) != 1 || lc.calls[0].unitIDs[0] != "target-3-v2" {
		t.Errorf("superseded unitIDs = %v, want exactly [target-3-v2] (v1 was already superseded and must not be re-touched)", lc.calls[0].unitIDs)
	}

	var changedAt string
	if err := svc.store.db.QueryRow(`SELECT lifecycle_changed_at FROM knowledge_units WHERE unit_id = 'target-3-v1'`).Scan(&changedAt); err != nil {
		t.Fatalf("query v1 lifecycle_changed_at: %v", err)
	}
	if !strings.HasPrefix(changedAt, "2026-01-01") {
		t.Errorf("v1 lifecycle_changed_at = %q, want unchanged (2026-01-01, not refreshed by the second reupload)", changedAt)
	}
}

// TestSwapShadowIntoTarget_ReplacesOutlinesInsteadOfDuplicating is the
// regression test for a real incident: outlines have no lifecycle field
// (unlike units/points, filtered purely by deletion per
// docs/impl/v1/lifecycle.md 步骤 3), so the swap must delete the target's own
// pre-reupload outline rows before reparenting the shadow's — otherwise both
// sets coexist under the same source_id (observed in production: one
// reuploaded source ended up with 39 duplicate outline rows out of 104).
func TestSwapShadowIntoTarget_ReplacesOutlinesInsteadOfDuplicating(t *testing.T) {
	svc, _ := setupTestService(t)

	svc.store.Create(&Source{
		SourceID: "target-1", Title: "Target Title", Format: "markdown", FileName: "old.md",
		OriginalPath: "o/target-1.md", MarkdownPath: "m/target-1.md", Status: "completed",
	})
	svc.store.Create(&Source{
		SourceID: "shadow-1", Title: "Shadow Title", Format: "markdown", FileName: "new.md",
		OriginalPath: "o/shadow-1.md", MarkdownPath: "m/shadow-1.md", Status: "completed",
		ShadowOf: sql.NullString{String: "target-1", Valid: true},
	})

	if err := svc.store.InsertOutline(&Outline{
		OutlineID: "old-outline-1", SourceID: "target-1", Level: 1,
		Title: "旧版标题", LineStart: 1, LineEnd: 5, NodeType: "structural",
	}); err != nil {
		t.Fatalf("insert old outline: %v", err)
	}
	if err := svc.store.InsertOutline(&Outline{
		OutlineID: "new-outline-1", SourceID: "shadow-1", Level: 1,
		Title: "新版标题", LineStart: 1, LineEnd: 8, NodeType: "structural",
	}); err != nil {
		t.Fatalf("insert new outline: %v", err)
	}

	if err := svc.store.SwapShadowIntoTarget("shadow-1", "target-1", "o/target-1-new.md", sql.NullString{}); err != nil {
		t.Fatalf("SwapShadowIntoTarget: %v", err)
	}

	outlines, err := svc.store.GetOutlines("target-1")
	if err != nil {
		t.Fatalf("GetOutlines: %v", err)
	}
	if len(outlines) != 1 {
		t.Fatalf("got %d outlines under target-1 after swap, want 1 (old one replaced, not duplicated): %+v", len(outlines), outlines)
	}
	if outlines[0].OutlineID != "new-outline-1" || outlines[0].Title != "新版标题" {
		t.Errorf("surviving outline = %+v, want the shadow's new-outline-1", outlines[0])
	}
}
