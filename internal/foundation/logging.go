package foundation

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

func InitLogger(logDir string, level slog.Level) (*slog.Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "wiki-brain.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0644,
	)
	if err != nil {
		return nil, err
	}

	w := io.MultiWriter(os.Stdout, logFile)
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger, nil
}
