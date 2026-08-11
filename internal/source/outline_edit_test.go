package source

import (
	"database/sql"
	"testing"
)

func insertTestSourceForOutline(t *testing.T, svc *Service, fileName string) string {
	t.Helper()
	src := &Source{
		Title:    "test source",
		Format:   "markdown",
		FileName: fileName,
		Status:   "processed",
	}
	if err := svc.store.Create(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	return src.SourceID
}

func TestUpdateOutlineSummary_Success(t *testing.T) {
	svc, _ := setupTestService(t)

	sourceID := insertTestSourceForOutline(t, svc, "a.md")
	outline := &Outline{
		SourceID:  sourceID,
		Level:     1,
		Title:     "第一章",
		Summary:   sql.NullString{String: "旧摘要", Valid: true},
		LineStart: 1,
		LineEnd:   10,
		NodeType:  "structural",
	}
	if err := svc.store.InsertOutline(outline); err != nil {
		t.Fatalf("insert outline: %v", err)
	}

	updated, err := svc.UpdateOutlineSummary(sourceID, outline.OutlineID, "新摘要")
	if err != nil {
		t.Fatalf("UpdateOutlineSummary: %v", err)
	}
	if !updated.Summary.Valid || updated.Summary.String != "新摘要" {
		t.Fatalf("expected summary to be updated, got %+v", updated.Summary)
	}

	// verify persisted
	got, err := svc.store.GetOutlineByID(sourceID, outline.OutlineID)
	if err != nil {
		t.Fatalf("GetOutlineByID: %v", err)
	}
	if got.Summary.String != "新摘要" {
		t.Fatalf("expected persisted summary '新摘要', got %q", got.Summary.String)
	}
}

func TestUpdateOutlineSummary_OutlineNotFound(t *testing.T) {
	svc, _ := setupTestService(t)

	sourceID := insertTestSourceForOutline(t, svc, "b.md")

	_, err := svc.UpdateOutlineSummary(sourceID, "nonexistent-outline-id", "新摘要")
	if err == nil {
		t.Fatal("expected error for nonexistent outline id, got nil")
	}
}

func TestUpdateOutlineSummary_WrongSource(t *testing.T) {
	svc, _ := setupTestService(t)

	sourceID1 := insertTestSourceForOutline(t, svc, "c.md")
	sourceID2 := insertTestSourceForOutline(t, svc, "d.md")

	outline := &Outline{
		SourceID:  sourceID1,
		Level:     1,
		Title:     "第一章",
		LineStart: 1,
		LineEnd:   10,
		NodeType:  "structural",
	}
	if err := svc.store.InsertOutline(outline); err != nil {
		t.Fatalf("insert outline: %v", err)
	}

	// try to update outline belonging to sourceID1, but scoped under sourceID2
	_, err := svc.UpdateOutlineSummary(sourceID2, outline.OutlineID, "越权摘要")
	if err == nil {
		t.Fatal("expected error when outline does not belong to source_id, got nil")
	}
}
