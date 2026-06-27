package foundation

import (
	"database/sql"
	"path/filepath"
	"testing"

	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
)

func NewTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := fdb.Open(dbPath)
	if err != nil {
		t.Fatalf("NewTestDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func NewTestDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
