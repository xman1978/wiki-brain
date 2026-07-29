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

// TestLoadPresetData_AliasesFeedSubjectSynonyms covers
// docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md:
// concept aliases in domains.json should load as active, source=preset
// rows in subject_synonyms with no additional authoring effort.
func TestLoadPresetData_AliasesFeedSubjectSynonyms(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	presetJSON := `{
		"domains": [
			{
				"id": "finance_stocks",
				"name": "金融股票",
				"description": "",
				"concepts": [
					{"id": "stock_market", "name": "股票市场", "aliases": ["证券市场", "二级市场"], "description": ""}
				]
			}
		]
	}`
	presetPath := filepath.Join(t.TempDir(), "domains.json")
	os.WriteFile(presetPath, []byte(presetJSON), 0644)

	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("LoadPresetData: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subject_synonyms WHERE status = 'active' AND source = 'preset'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("active preset synonyms = %d, want 2", count)
	}

	var canonical string
	if err := db.QueryRow(`SELECT canonical FROM subject_synonyms WHERE term = '证券市场'`).Scan(&canonical); err != nil {
		t.Fatalf("query canonical for 证券市场: %v", err)
	}
	if canonical != "股票市场" {
		t.Errorf("canonical = %q, want 股票市场", canonical)
	}
}

// TestLoadPresetData_AliasReloadRefreshesButDoesNotClobberGapMined covers the
// re-run/protect rule: replaying preset refreshes its own rows' canonical,
// but never overwrites a term that a gap-mined/manual active row already owns.
func TestLoadPresetData_AliasReloadRefreshesButDoesNotClobberGapMined(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	presetJSON := `{
		"domains": [
			{
				"id": "d1", "name": "D1", "description": "",
				"concepts": [
					{"id": "c1", "name": "规范名称", "aliases": ["别名词"], "description": ""}
				]
			}
		]
	}`
	presetPath := filepath.Join(t.TempDir(), "domains.json")
	os.WriteFile(presetPath, []byte(presetJSON), 0644)

	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("initial LoadPresetData: %v", err)
	}

	// A human has since confirmed a gap-mined mapping for the same term to a
	// different canonical — preset replay must not clobber it.
	if _, err := db.Exec(
		`UPDATE subject_synonyms SET source = 'gap_mined', canonical = '人工确认规范' WHERE term = '别名词'`,
	); err != nil {
		t.Fatal(err)
	}

	if err := LoadPresetData(db, presetPath); err != nil {
		t.Fatalf("second LoadPresetData: %v", err)
	}

	var canonical, source string
	if err := db.QueryRow(`SELECT canonical, source FROM subject_synonyms WHERE term = '别名词'`).Scan(&canonical, &source); err != nil {
		t.Fatal(err)
	}
	if source != "gap_mined" || canonical != "人工确认规范" {
		t.Errorf("got source=%q canonical=%q, want gap_mined / 人工确认规范 (preset must not clobber)", source, canonical)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM subject_synonyms WHERE term = '别名词'`).Scan(&count)
	if count != 1 {
		t.Errorf("count = %d, want 1 (no duplicate row)", count)
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
