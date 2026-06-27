package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify WAL mode
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	// Verify foreign_keys
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	// Verify domains table exists
	_, err = db.Exec("INSERT INTO domains (domain_id, name) VALUES ('test', 'Test Domain')")
	if err != nil {
		t.Fatalf("insert into domains: %v", err)
	}

	// Verify concepts table with foreign key
	_, err = db.Exec("INSERT INTO concepts (concept_id, domain_id, name) VALUES ('c1', 'test', 'Test Concept')")
	if err != nil {
		t.Fatalf("insert into concepts: %v", err)
	}

	// Verify foreign key constraint
	_, err = db.Exec("INSERT INTO concepts (concept_id, domain_id, name) VALUES ('c2', 'nonexistent', 'Bad')")
	if err == nil {
		t.Fatal("expected foreign key error")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count < 1 {
		t.Errorf("migration count = %d, want >= 1", count)
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sub", "dir", "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	db.Close()
}
