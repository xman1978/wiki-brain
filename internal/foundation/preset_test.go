package foundation

import (
	"os"
	"path/filepath"
	"testing"

	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
)

func TestLoadPresetData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	presetJSON := `{
		"domains": [
			{
				"id": "se",
				"name": "软件工程",
				"description": "软件设计与开发",
				"concepts": [
					{"id": "deploy", "name": "部署", "description": "应用部署"},
					{"id": "arch", "name": "架构", "description": "系统架构"}
				]
			}
		]
	}`

	presetPath := filepath.Join(t.TempDir(), "domains.json")
	os.WriteFile(presetPath, []byte(presetJSON), 0644)

	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("LoadPresetData: %v", err)
	}

	var domainCount int
	db.QueryRow("SELECT COUNT(*) FROM domains").Scan(&domainCount)
	if domainCount != 1 {
		t.Errorf("domains = %d, want 1", domainCount)
	}

	var conceptCount int
	db.QueryRow("SELECT COUNT(*) FROM concepts").Scan(&conceptCount)
	if conceptCount != 2 {
		t.Errorf("concepts = %d, want 2", conceptCount)
	}
}

func TestLoadPresetDataIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	presetJSON := `{"domains":[{"id":"d1","name":"D1","description":"","concepts":[]}]}`
	presetPath := filepath.Join(t.TempDir(), "domains.json")
	os.WriteFile(presetPath, []byte(presetJSON), 0644)

	LoadPresetData(db, presetPath)
	LoadPresetData(db, presetPath)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM domains").Scan(&count)
	if count != 1 {
		t.Errorf("domains = %d after double load, want 1", count)
	}
}

// TestLoadPresetData_DoesNotReviveMergedConcept_OrRewriteOrigin covers
// docs/impl/v1/concept-evolution.md 步骤 4's preset UPSERT rule: replaying
// preset for a concept_id that was merged away, or evolved via human
// confirmation, must update name/description only — merged_into and origin
// are untouched.
func TestLoadPresetData_DoesNotReviveMergedConcept_OrRewriteOrigin(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	presetJSON := `{
		"domains": [
			{
				"id": "se",
				"name": "软件工程",
				"description": "软件设计与开发",
				"concepts": [
					{"id": "merged-away", "name": "被合并概念", "description": "旧描述"},
					{"id": "evolved-one", "name": "演化概念", "description": "旧描述2"}
				]
			}
		]
	}`
	presetPath := filepath.Join(t.TempDir(), "domains.json")
	os.WriteFile(presetPath, []byte(presetJSON), 0644)

	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("initial LoadPresetData: %v", err)
	}

	// Simulate concept evolution having already acted on these two rows:
	// one merged into another concept, one confirmed as human-added.
	if _, err := db.Exec(`INSERT INTO concepts (concept_id, domain_id, name, origin) VALUES ('target', 'se', 'Target', 'preset')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE concepts SET merged_into = 'target' WHERE concept_id = 'merged-away'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE concepts SET origin = 'evolved' WHERE concept_id = 'evolved-one'`); err != nil {
		t.Fatal(err)
	}

	// Re-run preset with updated name/description for both rows.
	presetJSON2 := `{
		"domains": [
			{
				"id": "se",
				"name": "软件工程",
				"description": "软件设计与开发",
				"concepts": [
					{"id": "merged-away", "name": "被合并概念新名", "description": "新描述"},
					{"id": "evolved-one", "name": "演化概念新名", "description": "新描述2"}
				]
			}
		]
	}`
	os.WriteFile(presetPath, []byte(presetJSON2), 0644)
	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("second LoadPresetData: %v", err)
	}

	var mergedInto, name string
	if err := db.QueryRow(`SELECT merged_into, name FROM concepts WHERE concept_id = 'merged-away'`).Scan(&mergedInto, &name); err != nil {
		t.Fatal(err)
	}
	if mergedInto != "target" {
		t.Errorf("merged_into = %q, want target (must not be revived by preset replay)", mergedInto)
	}
	if name != "被合并概念新名" {
		t.Errorf("name = %q, want updated name (preset should still refresh name/description)", name)
	}

	var origin string
	if err := db.QueryRow(`SELECT origin FROM concepts WHERE concept_id = 'evolved-one'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "evolved" {
		t.Errorf("origin = %q, want evolved (preset must not rewrite origin)", origin)
	}
}

func TestLoadPresetDataFileNotFound(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = LoadPresetData(db, "/nonexistent/domains.json")
	if err != nil {
		t.Errorf("should not error on missing file, got: %v", err)
	}
}
