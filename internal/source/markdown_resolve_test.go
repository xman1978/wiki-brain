package source

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveMarkdownPathForUnit_CurrentUsesSourcePath(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.store.Create(&Source{
		SourceID: "src-cur", Title: "T", Format: "markdown", FileName: "a.md",
		OriginalPath: "o/a.md", MarkdownPath: "data/sources/markdown/src-cur.md", Status: "completed",
	})

	got, err := svc.store.ResolveMarkdownPathForUnit("src-cur", "current", sql.NullTime{})
	if err != nil {
		t.Fatalf("ResolveMarkdownPathForUnit: %v", err)
	}
	if got != "data/sources/markdown/src-cur.md" {
		t.Errorf("path = %q, want current sources.markdown_path", got)
	}
}

func TestResolveMarkdownPathForUnit_SupersededUsesArchivedVersion(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.store.Create(&Source{
		SourceID: "src-sup", Title: "T", Format: "markdown", FileName: "a.md",
		OriginalPath: "o/a.md", MarkdownPath: "data/sources/markdown/src-sup.md", Status: "completed",
	})

	changedAt := time.Date(2026, 7, 22, 3, 14, 50, 0, time.UTC)
	archivedAt := time.Date(2026, 7, 22, 3, 14, 56, 0, time.UTC)
	archivedMD := filepath.Join("data", "sources", "archived", "src-sup", "20260722T031456Z", "src-sup.md")
	if err := svc.store.InsertSourceVersion(&SourceVersion{
		SourceID:     "src-sup",
		Version:      1,
		FileName:     "a.md",
		OriginalPath: archivedMD,
		MarkdownPath: archivedMD,
	}); err != nil {
		t.Fatalf("InsertSourceVersion: %v", err)
	}
	// Force archived_at so the query can match lifecycle_changed_at reliably.
	if _, err := svc.store.db.Exec(
		`UPDATE source_versions SET archived_at = ? WHERE source_id = ? AND version = 1`,
		archivedAt.Format("2006-01-02 15:04:05"), "src-sup",
	); err != nil {
		t.Fatalf("update archived_at: %v", err)
	}

	got, err := svc.store.ResolveMarkdownPathForUnit(
		"src-sup",
		"superseded",
		sql.NullTime{Time: changedAt, Valid: true},
	)
	if err != nil {
		t.Fatalf("ResolveMarkdownPathForUnit: %v", err)
	}
	if got != archivedMD {
		t.Errorf("path = %q, want archived %q", got, archivedMD)
	}
}

func TestResolveMarkdownPathForUnit_SupersededFallbackWhenNoVersion(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.store.Create(&Source{
		SourceID: "src-fb", Title: "T", Format: "markdown", FileName: "a.md",
		OriginalPath: "o/a.md", MarkdownPath: "data/sources/markdown/src-fb.md", Status: "completed",
	})

	got, err := svc.store.ResolveMarkdownPathForUnit(
		"src-fb",
		"superseded",
		sql.NullTime{Time: time.Now().UTC(), Valid: true},
	)
	if err != nil {
		t.Fatalf("ResolveMarkdownPathForUnit: %v", err)
	}
	if got != "data/sources/markdown/src-fb.md" {
		t.Errorf("path = %q, want current fallback", got)
	}
}

func TestResolveMarkdownPathByUnitID_Superseded(t *testing.T) {
	svc, _ := setupTestService(t)
	svc.store.Create(&Source{
		SourceID: "src-by", Title: "T", Format: "markdown", FileName: "a.md",
		OriginalPath: "o/a.md", MarkdownPath: "data/sources/markdown/src-by.md", Status: "completed",
	})
	changedAt := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	archivedAt := time.Date(2026, 7, 22, 1, 0, 5, 0, time.UTC)
	archivedMD := "data/sources/archived/src-by/v1.md"
	if err := svc.store.InsertSourceVersion(&SourceVersion{
		SourceID: "src-by", Version: 1, FileName: "a.md",
		OriginalPath: archivedMD, MarkdownPath: archivedMD,
	}); err != nil {
		t.Fatalf("InsertSourceVersion: %v", err)
	}
	if _, err := svc.store.db.Exec(
		`UPDATE source_versions SET archived_at = ? WHERE source_id = ?`,
		archivedAt.Format("2006-01-02 15:04:05"), "src-by",
	); err != nil {
		t.Fatalf("update archived_at: %v", err)
	}
	if _, err := svc.store.db.Exec(`INSERT INTO knowledge_units
		(unit_id, source_id, center, line_start, line_end, status, prompt_version, lifecycle, lifecycle_changed_at)
		VALUES ('u-old', 'src-by', 'c', 1, 2, 'completed', 'v1', 'superseded', ?)`,
		changedAt.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	got, err := svc.store.ResolveMarkdownPathByUnitID("u-old")
	if err != nil {
		t.Fatalf("ResolveMarkdownPathByUnitID: %v", err)
	}
	if got != archivedMD {
		t.Errorf("path = %q, want %q", got, archivedMD)
	}
}
