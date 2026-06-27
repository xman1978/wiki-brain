package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupAndRestore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	db.Exec("CREATE TABLE test_data (id INTEGER PRIMARY KEY, value TEXT)")
	db.Exec("INSERT INTO test_data (id, value) VALUES (1, 'hello')")

	backupDir := filepath.Join(tmpDir, "backups")
	backupPath, err := Backup(db, dbPath, BackupConfig{BackupDir: backupDir, MaxKeep: 3})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file not found: %v", err)
	}

	db.Exec("INSERT INTO test_data (id, value) VALUES (2, 'world')")

	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_data").Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 rows before restore, got %d", count)
	}

	db.Close()

	if err := Restore(backupPath, dbPath); err != nil {
		t.Fatalf("restore: %v", err)
	}

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()

	db2.QueryRow("SELECT COUNT(*) FROM test_data").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after restore, got %d", count)
	}
}

func TestBackupRotation(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	backupDir := filepath.Join(tmpDir, "backups")
	for i := 0; i < 5; i++ {
		_, err := Backup(db, dbPath, BackupConfig{BackupDir: backupDir, MaxKeep: 3})
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}

	files, err := ListBackups(backupDir)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 backups after rotation, got %d", len(files))
	}
}

func TestLatestBackup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	backupDir := filepath.Join(tmpDir, "backups")
	var lastPath string
	for i := 0; i < 3; i++ {
		lastPath, _ = Backup(db, dbPath, BackupConfig{BackupDir: backupDir, MaxKeep: 5})
	}

	latest, err := LatestBackup(backupDir)
	if err != nil {
		t.Fatalf("latest backup: %v", err)
	}
	if latest != lastPath {
		t.Errorf("latest = %s, want %s", latest, lastPath)
	}
}

func TestOpenSafe_Healthy(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Exec("CREATE TABLE t (id INTEGER)")
	db.Close()

	db2, err := OpenSafe(dbPath, filepath.Join(tmpDir, "backups"))
	if err != nil {
		t.Fatalf("open safe: %v", err)
	}
	defer db2.Close()

	var count int
	db2.QueryRow("SELECT COUNT(*) FROM t").Scan(&count)
}

func TestOpenSafe_CorruptedWithBackup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Exec("CREATE TABLE t (id INTEGER)")
	db.Exec("INSERT INTO t VALUES (1)")

	backupDir := filepath.Join(tmpDir, "backups")
	_, err = Backup(db, dbPath, BackupConfig{BackupDir: backupDir, MaxKeep: 3})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	db.Close()

	// Corrupt the database
	os.WriteFile(dbPath, []byte("corrupted data here"), 0644)

	db2, err := OpenSafe(dbPath, backupDir)
	if err != nil {
		t.Fatalf("open safe should restore from backup: %v", err)
	}
	defer db2.Close()

	var count int
	db2.QueryRow("SELECT COUNT(*) FROM t").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after restore, got %d", count)
	}
}

func TestOpenSafe_CorruptedNoBackup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	os.WriteFile(dbPath, []byte("corrupted"), 0644)

	_, err := OpenSafe(dbPath, filepath.Join(tmpDir, "backups"))
	if err == nil {
		t.Fatal("expected error when corrupted with no backup")
	}
}

func TestCheckIntegrity(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := CheckIntegrity(db); err != nil {
		t.Errorf("integrity check should pass: %v", err)
	}
}
