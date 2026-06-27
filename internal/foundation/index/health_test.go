package index

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func TestCheckHealth_EmptyDB(t *testing.T) {
	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()
	mgr, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	defer mgr.Close()

	status := mgr.CheckHealth(db)
	// Empty DB + empty index = healthy (no mismatch)
	if !status.Healthy {
		t.Errorf("expected healthy for empty state, issues=%v", status.Issues)
	}
	if !status.Units.Searchable {
		t.Error("units should be searchable after gse init")
	}
}

func TestCheckHealth_Mismatch(t *testing.T) {
	db := foundation.NewTestDB(t)
	// Insert a completed unit but don't index it
	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('s1', 'test', 'md', 'test.md', '/tmp/test.md', '/tmp/test.md', 'completed')`)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u1', 's1', 'test', 1, 10, 'completed', 'v1')`)

	tmpDir := t.TempDir()
	mgr, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	defer mgr.Close()

	status := mgr.CheckHealth(db)
	if status.Healthy {
		t.Error("should be unhealthy: DB has unit but index is empty")
	}
	if len(status.Issues) == 0 {
		t.Error("expected issues")
	}
}

func TestProbeSearchable(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	if !probeSearchable(mgr.Units) {
		t.Error("units should be searchable with initialized gse")
	}

	// Verify probe doc was cleaned up
	count, _ := mgr.Units.DocCount()
	if count != 0 {
		t.Errorf("probe doc should be deleted, count=%d", count)
	}
}
