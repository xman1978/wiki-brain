package unit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractStartsUnitsProcessingAfterAcquiringOwnership(t *testing.T) {
	svc, _, db := setupTestService(t)
	mdPath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(mdPath, nil, 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-owned", mdPath)

	if err := svc.Extract(t.Context(), "src-owned"); err != nil {
		t.Fatalf("extract empty source: %v", err)
	}

	got, err := svc.sourceStore.GetByID("src-owned")
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if got.UnitsStatus != "processing" || got.UnitsStage != "building" {
		t.Fatalf("units status/stage = %q/%q, want processing/building", got.UnitsStatus, got.UnitsStage)
	}
}

func TestExtractDuplicateDoesNotResetOwnedSemanticProgress(t *testing.T) {
	svc, _, db := setupTestService(t)
	insertSource(t, db, "src-duplicate", "/tmp/unused.md")

	if err := svc.sourceStore.StartUnitsProcessing("src-duplicate"); err != nil {
		t.Fatalf("start units processing: %v", err)
	}
	if err := svc.sourceStore.MarkUnitsSemanticsStarted("src-duplicate"); err != nil {
		t.Fatalf("mark semantics started: %v", err)
	}
	if !svc.beginExtract("src-duplicate") {
		t.Fatal("set existing extraction ownership")
	}
	defer svc.endExtract("src-duplicate")

	err := svc.Extract(t.Context(), "src-duplicate")
	if !errors.Is(err, ErrExtractionInProgress) {
		t.Fatalf("extract error = %v, want ErrExtractionInProgress", err)
	}

	got, err := svc.sourceStore.GetByID("src-duplicate")
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if got.UnitsStatus != "processing" || got.UnitsStage != "semantics" || !got.UnitsBuiltAt.Valid {
		t.Fatalf("duplicate changed semantic progress: status=%q stage=%q built_at.valid=%v", got.UnitsStatus, got.UnitsStage, got.UnitsBuiltAt.Valid)
	}
}
