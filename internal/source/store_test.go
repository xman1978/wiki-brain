package source

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func TestStoreCreateAndGet(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title:        "Test Document",
		Format:       "markdown",
		FileName:     "test.md",
		OriginalPath: "data/sources/original/abc.md",
		MarkdownPath: "data/sources/markdown/abc.md",
		Status:       "pending",
	}
	if err := store.Create(src); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if src.SourceID == "" {
		t.Fatal("SourceID should be assigned")
	}

	got, err := store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Title != "Test Document" {
		t.Errorf("title = %q, want %q", got.Title, "Test Document")
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want %q", got.Status, "pending")
	}
}

func TestStoreUpdateStatus(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title: "Test", Format: "pdf", FileName: "test.pdf",
		OriginalPath: "o/t.pdf", MarkdownPath: "m/t.md", Status: "pending",
	}
	store.Create(src)

	if err := store.UpdateStatus(src.SourceID, "processing", nil); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := store.GetByID(src.SourceID)
	if got.Status != "processing" {
		t.Errorf("status = %q, want processing", got.Status)
	}

	errMsg := "conversion failed"
	if err := store.UpdateStatus(src.SourceID, "failed", &errMsg); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
	got, _ = store.GetByID(src.SourceID)
	if got.Status != "failed" || !got.ErrorMsg.Valid || got.ErrorMsg.String != errMsg {
		t.Errorf("status=%q error_msg=%v, want failed/%q", got.Status, got.ErrorMsg, errMsg)
	}
}

func TestStoreUpdateUnitsStatus(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title: "Test", Format: "pdf", FileName: "test.pdf",
		OriginalPath: "o/t.pdf", MarkdownPath: "m/t.md", Status: "completed",
	}
	store.Create(src)

	got, _ := store.GetByID(src.SourceID)
	if got.UnitsStatus != "pending" {
		t.Errorf("initial units_status = %q, want pending (column default)", got.UnitsStatus)
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "processing"); err != nil {
		t.Fatalf("UpdateUnitsStatus: %v", err)
	}
	got, _ = store.GetByID(src.SourceID)
	if got.UnitsStatus != "processing" {
		t.Errorf("units_status = %q, want processing", got.UnitsStatus)
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "completed"); err != nil {
		t.Fatalf("UpdateUnitsStatus: %v", err)
	}
	got, _ = store.GetByID(src.SourceID)
	if got.UnitsStatus != "completed" {
		t.Errorf("units_status = %q, want completed", got.UnitsStatus)
	}
	if !got.UnitsCompletedAt.Valid {
		t.Errorf("units_completed_at not set after units_status=completed")
	}

	// List must surface it too — it's a separate SELECT/scan from GetByID.
	list, err := store.List("", "", 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].UnitsStatus != "completed" || !list[0].UnitsCompletedAt.Valid {
		t.Errorf("List()[0] = %+v, want units_status=completed and units_completed_at set", list)
	}
}

func TestStoreUpdateUnitsStatusClearsCompletedAtForNonTerminalStatus(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title: "Test", Format: "pdf", FileName: "test.pdf",
		OriginalPath: "o/t.pdf", MarkdownPath: "m/t.md", Status: "completed",
	}
	if err := store.Create(src); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "completed"); err != nil {
		t.Fatalf("complete units: %v", err)
	}
	got, err := store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get completed source: %v", err)
	}
	if !got.UnitsCompletedAt.Valid {
		t.Fatal("units_completed_at should be set after units_status=completed")
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "processing"); err != nil {
		t.Fatalf("restart units: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get processing source: %v", err)
	}
	if got.UnitsCompletedAt.Valid {
		t.Fatal("units_completed_at should be NULL after a non-terminal status")
	}
}

func TestStoreUnitStageLifecycle(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		SourceID:     "src-stage-1",
		Title:        "Doc",
		Format:       "md",
		FileName:     "doc.md",
		OriginalPath: "/tmp/doc.md",
		MarkdownPath: "/tmp/doc.md",
		Status:       "completed",
	}
	if err := store.Create(src); err != nil {
		t.Fatalf("create source: %v", err)
	}

	got, err := store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get initial source: %v", err)
	}
	if got.UnitsStage != "pending" {
		t.Fatalf("initial units_stage = %q, want pending", got.UnitsStage)
	}
	if got.UnitsBuiltAt.Valid {
		t.Fatalf("initial units_built_at should be NULL")
	}

	if err := store.StartUnitsProcessing(src.SourceID); err != nil {
		t.Fatalf("start units processing: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get building source: %v", err)
	}
	if got.UnitsStatus != "processing" || got.UnitsStage != "building" {
		t.Fatalf("after start units_status=%q units_stage=%q, want processing/building", got.UnitsStatus, got.UnitsStage)
	}
	if got.UnitsBuiltAt.Valid || got.UnitsCompletedAt.Valid {
		t.Fatalf("start should clear units_built_at and units_completed_at")
	}

	if err := store.MarkUnitsSemanticsStarted(src.SourceID); err != nil {
		t.Fatalf("mark semantics started: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get semantics source: %v", err)
	}
	if got.UnitsStage != "semantics" {
		t.Fatalf("units_stage = %q, want semantics", got.UnitsStage)
	}
	if !got.UnitsBuiltAt.Valid {
		t.Fatalf("units_built_at should be set")
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "completed"); err != nil {
		t.Fatalf("complete units: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get completed source: %v", err)
	}
	if got.UnitsStatus != "completed" || got.UnitsStage != "semantics" {
		t.Fatalf("after complete units_status=%q units_stage=%q, want completed/semantics", got.UnitsStatus, got.UnitsStage)
	}
	if !got.UnitsCompletedAt.Valid {
		t.Fatalf("units_completed_at should be set")
	}

	list, err := store.List("", "", 10, 0)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if list[0].UnitsStage != "semantics" || !list[0].UnitsBuiltAt.Valid {
		t.Fatalf("List()[0] units_stage=%q units_built_at.Valid=%v, want semantics/true", list[0].UnitsStage, list[0].UnitsBuiltAt.Valid)
	}
}

func TestStoreList(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	for _, name := range []string{"a.md", "b.pdf", "c.docx"} {
		store.Create(&Source{
			Title: name, Format: "markdown", FileName: name,
			OriginalPath: "o/" + name, MarkdownPath: "m/" + name, Status: "pending",
		})
	}

	all, err := store.List("", "", 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want 3", len(all))
	}
}

func TestStoreOutlines(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/t.md", MarkdownPath: "m/t.md", Status: "completed",
	}
	store.Create(src)

	outlines := []Outline{
		{SourceID: src.SourceID, Level: 1, Title: "Chapter 1", LineStart: 1, LineEnd: 50, NodeType: "structural", Position: 0},
		{SourceID: src.SourceID, Level: 2, Title: "Section 1.1", LineStart: 3, LineEnd: 25, NodeType: "structural", Position: 0},
		{SourceID: src.SourceID, Level: 2, Title: "Section 1.2", LineStart: 26, LineEnd: 50, NodeType: "structural", Position: 1},
	}
	// Set parent_id for children
	outlines[0].OutlineID = "root-1"
	outlines[1].ParentID = sql.NullString{String: "root-1", Valid: true}
	outlines[2].ParentID = sql.NullString{String: "root-1", Valid: true}

	if err := store.InsertOutlines(outlines); err != nil {
		t.Fatalf("InsertOutlines: %v", err)
	}

	got, err := store.GetOutlines(src.SourceID)
	if err != nil {
		t.Fatalf("GetOutlines: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}

	tree, err := store.GetOutlineTree(src.SourceID)
	if err != nil {
		t.Fatalf("GetOutlineTree: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("root count = %d, want 1", len(tree))
	}
	if len(tree[0].Children) != 2 {
		t.Errorf("children count = %d, want 2", len(tree[0].Children))
	}

	// Test delete and re-insert
	if err := store.DeleteOutlines(src.SourceID); err != nil {
		t.Fatalf("DeleteOutlines: %v", err)
	}
	got, _ = store.GetOutlines(src.SourceID)
	if len(got) != 0 {
		t.Errorf("after delete: len = %d, want 0", len(got))
	}
}

func TestStoreUpdateSummaryAndDomain(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	src := &Source{
		Title: "Test", Format: "markdown", FileName: "test.md",
		OriginalPath: "o/t.md", MarkdownPath: "m/t.md", Status: "completed",
	}
	store.Create(src)

	if err := store.UpdateSummary(src.SourceID, "This is a summary"); err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}
	got, _ := store.GetByID(src.SourceID)
	if !got.Summary.Valid || got.Summary.String != "This is a summary" {
		t.Errorf("summary = %v, want 'This is a summary'", got.Summary)
	}

	// Insert a domain first to satisfy FK
	db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('test-domain', 'Test')`)
	domainID := "test-domain"
	if err := store.UpdateDomainID(src.SourceID, &domainID); err != nil {
		t.Fatalf("UpdateDomainID: %v", err)
	}
	got, _ = store.GetByID(src.SourceID)
	if !got.DomainID.Valid || got.DomainID.String != "test-domain" {
		t.Errorf("domain_id = %v, want 'test-domain'", got.DomainID)
	}
}
