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
