package db

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigration052_WikiSynthesisSatisfaction verifies the four additive
// synthesis-axis columns on wiki_pages exist post-migration and default to 0
// on both a pre-existing row (added via ALTER TABLE ... DEFAULT 0) and a
// freshly-inserted one (docs/impl/v1/wiki.md 步骤 4a).
func TestMigration052_WikiSynthesisSatisfaction(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var target string
	for _, name := range files {
		if strings.HasPrefix(name, "052_") {
			target = name
			break
		}
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := database.Exec(string(content)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	if target == "" {
		t.Fatal("052_wiki_synthesis_satisfaction.sql not found in embedded migrations")
	}

	// Seed a pre-migration page row.
	if _, err := database.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, title, content, prompt_version, model_name)
		VALUES ('p1', 'concept', 't', 'c', 'v1', 'm1')`); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	content, err := migrationsFS.ReadFile("migrations/" + target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if _, err := database.Exec(string(content)); err != nil {
		t.Fatalf("apply migration 052: %v", err)
	}

	var success, failure, auditedSuccess, auditedFailure int
	if err := database.QueryRow(`SELECT synthesis_success_count, synthesis_failure_count,
		synthesis_audited_success_count, synthesis_audited_failure_count
		FROM wiki_pages WHERE page_id = 'p1'`).Scan(&success, &failure, &auditedSuccess, &auditedFailure); err != nil {
		t.Fatalf("query synthesis columns on pre-existing row: %v", err)
	}
	if success != 0 || failure != 0 || auditedSuccess != 0 || auditedFailure != 0 {
		t.Errorf("pre-existing row synthesis counts = (%d,%d,%d,%d), want all 0", success, failure, auditedSuccess, auditedFailure)
	}

	// A freshly-inserted row (post-migration) must also default to 0 without
	// specifying the new columns.
	if _, err := database.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, title, content, prompt_version, model_name)
		VALUES ('p2', 'concept', 't2', 'c2', 'v1', 'm1')`); err != nil {
		t.Fatalf("seed post-migration page: %v", err)
	}
	if err := database.QueryRow(`SELECT synthesis_success_count, synthesis_failure_count,
		synthesis_audited_success_count, synthesis_audited_failure_count
		FROM wiki_pages WHERE page_id = 'p2'`).Scan(&success, &failure, &auditedSuccess, &auditedFailure); err != nil {
		t.Fatalf("query synthesis columns on fresh row: %v", err)
	}
	if success != 0 || failure != 0 || auditedSuccess != 0 || auditedFailure != 0 {
		t.Errorf("fresh row synthesis counts = (%d,%d,%d,%d), want all 0", success, failure, auditedSuccess, auditedFailure)
	}
}
