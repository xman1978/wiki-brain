package foundation

import (
	"testing"
)

func TestNewTestDB(t *testing.T) {
	db := NewTestDB(t)

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count == 0 {
		t.Error("no migrations applied in test DB")
	}
}

func TestNewTestDir(t *testing.T) {
	dir := NewTestDir(t)
	if dir == "" {
		t.Error("empty dir")
	}
}
