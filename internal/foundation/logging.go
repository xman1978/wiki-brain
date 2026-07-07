package foundation

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LogOptions 对应 config.LoggingConfig，描述日志级别、输出目的地与轮转策略。
type LogOptions struct {
	Level      slog.Level
	Dir        string
	Filename   string
	Console    bool
	File       bool
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

func buildWriter(opts LogOptions) (io.Writer, error) {
	var writers []io.Writer

	if opts.Console {
		writers = append(writers, os.Stdout)
	}

	if opts.File {
		if err := os.MkdirAll(opts.Dir, 0755); err != nil {
			return nil, err
		}
		writers = append(writers, &lumberjack.Logger{
			Filename:   filepath.Join(opts.Dir, opts.Filename),
			MaxSize:    opts.MaxSizeMB,
			MaxBackups: opts.MaxBackups,
			MaxAge:     opts.MaxAgeDays,
			Compress:   opts.Compress,
		})
	}

	if len(writers) == 0 {
		return io.Discard, nil
	}
	return io.MultiWriter(writers...), nil
}

// InitLogger 初始化全局默认 logger（业务日志），并设为 slog.Default()。
func InitLogger(opts LogOptions) (*slog.Logger, error) {
	w, err := buildWriter(opts)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: opts.Level})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, nil
}

// NewAccessLogger 创建独立的访问日志 logger，与业务日志共用同一份文件（含轮转策略），
// 是否打印到控制台由 opts.Console（对应 config.LoggingConfig.AccessConsole）单独控制。
func NewAccessLogger(opts LogOptions) (*slog.Logger, error) {
	w, err := buildWriter(opts)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: opts.Level})
	return slog.New(handler), nil
}
