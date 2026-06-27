package foundation

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestInitLogger(t *testing.T) {
	dir := t.TempDir()

	logger, err := InitLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatalf("InitLogger: %v", err)
	}

	logger.Info("test message", "key", "value")

	logPath := filepath.Join(dir, "wiki-brain.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) == 0 {
		t.Error("log file is empty")
	}
}
