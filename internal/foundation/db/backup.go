package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BackupConfig struct {
	BackupDir string
	MaxKeep   int
}

func Backup(db *sql.DB, dbPath string, cfg BackupConfig) (string, error) {
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}
	if cfg.MaxKeep <= 0 {
		cfg.MaxKeep = 5
	}

	if err := os.MkdirAll(cfg.BackupDir, 0755); err != nil {
		return "", fmt.Errorf("db backup: create dir: %w", err)
	}

	ts := time.Now().Format("20060102_150405.000")
	backupPath := filepath.Join(cfg.BackupDir, fmt.Sprintf("backup_%s.db", ts))

	_, err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", backupPath))
	if err != nil {
		return "", fmt.Errorf("db backup: vacuum into: %w", err)
	}

	slog.Info("db backup created", "path", backupPath)

	if err := rotateBackups(cfg.BackupDir, cfg.MaxKeep); err != nil {
		slog.Warn("db backup: rotation failed", "error", err)
	}

	return backupPath, nil
}

func Restore(backupPath, dbPath string) error {
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("db restore: backup not found: %w", err)
	}

	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	os.Remove(walPath)
	os.Remove(shmPath)

	src, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("db restore: read backup: %w", err)
	}

	if err := os.WriteFile(dbPath, src, 0644); err != nil {
		return fmt.Errorf("db restore: write db: %w", err)
	}

	slog.Info("db restored from backup", "backup", backupPath, "db", dbPath)
	return nil
}

func LatestBackup(backupDir string) (string, error) {
	files, err := listBackups(backupDir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("db restore: no backups found in %s", backupDir)
	}
	return files[len(files)-1], nil
}

func ListBackups(backupDir string) ([]string, error) {
	return listBackups(backupDir)
}

// OpenSafe opens the database with integrity check. If the database is
// corrupted, it restores from the latest backup in backupDir and reopens.
// If no backup exists or restore fails, returns the original error.
func OpenSafe(dbPath, backupDir string) (*sql.DB, error) {
	db, err := Open(dbPath)
	if err != nil {
		return tryRestore(dbPath, backupDir, fmt.Errorf("open failed: %w", err))
	}

	if err := CheckIntegrity(db); err != nil {
		db.Close()
		return tryRestore(dbPath, backupDir, err)
	}

	slog.Info("db integrity check passed", "path", dbPath)
	return db, nil
}

func tryRestore(dbPath, backupDir string, origErr error) (*sql.DB, error) {
	slog.Error("db corrupted, attempting restore from backup", "error", origErr)

	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}

	backupPath, err := LatestBackup(backupDir)
	if err != nil {
		return nil, fmt.Errorf("db corrupted and no backup available: %w (original: %v)", err, origErr)
	}

	slog.Info("restoring from backup", "backup", backupPath)
	if err := Restore(backupPath, dbPath); err != nil {
		return nil, fmt.Errorf("db restore failed: %w (original: %v)", err, origErr)
	}

	db, err := Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("db reopen after restore failed: %w", err)
	}

	if err := CheckIntegrity(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("db still corrupted after restore: %w", err)
	}

	slog.Info("db restored and verified", "backup", backupPath)
	return db, nil
}

func CheckIntegrity(db *sql.DB) error {
	var result string
	err := db.QueryRow("PRAGMA integrity_check").Scan(&result)
	if err != nil {
		return fmt.Errorf("db integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("db integrity check failed: %s", result)
	}
	return nil
}

func rotateBackups(dir string, maxKeep int) error {
	files, err := listBackups(dir)
	if err != nil {
		return err
	}

	if len(files) <= maxKeep {
		return nil
	}

	toRemove := files[:len(files)-maxKeep]
	for _, f := range toRemove {
		if err := os.Remove(f); err != nil {
			slog.Warn("db backup: remove old backup failed", "path", f, "error", err)
		} else {
			slog.Info("db backup: removed old backup", "path", f)
		}
	}

	return nil
}

func listBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "backup_") && strings.HasSuffix(e.Name(), ".db") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}
