// Package sysconfig manages system settings that used to live in
// config.yml (文件转换服务、历史会话) and are now DB-backed and editable
// from the web page, taking effect immediately without a restart — same
// pattern as internal/llmconfig for model settings.
package sysconfig

// FileViewSettings controls document-conversion behavior
// (docs/impl/v1/local-file-convert.md). UseRemote selects between the
// remote FileView HTTP service and the built-in pure-Go local conversion
// fallback (previously fileview.mode: remote|local). OCR gates the
// scanned-PDF/image-file OCR fallback that only applies in local mode.
type FileViewSettings struct {
	UseRemote      bool   `json:"use_remote"`
	BaseURL        string `json:"base_url"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	PollIntervalMs int    `json:"poll_interval_ms"`
	OCREnabled     bool   `json:"ocr_enabled"`
	OCRMaxPages    int    `json:"ocr_max_pages"`
}

// SessionSettings controls the session-history retention scheduler.
type SessionSettings struct {
	RetentionDays   int `json:"retention_days"`
	CleanupInterval struct {
		Value int    `json:"value"`
		Unit  string `json:"unit"` // "hour" | "day"
	} `json:"cleanup_interval"`
}

// DefaultFileViewSettings mirrors the config.yml defaults this project
// shipped with before the migration to DB-backed settings.
func DefaultFileViewSettings() FileViewSettings {
	return FileViewSettings{
		UseRemote:      false,
		BaseURL:        "http://127.0.0.1:8000",
		TimeoutSeconds: 600,
		PollIntervalMs: 1500,
		OCREnabled:     true,
		OCRMaxPages:    50,
	}
}

// DefaultSessionSettings mirrors the config.yml defaults this project
// shipped with before the migration to DB-backed settings.
func DefaultSessionSettings() SessionSettings {
	s := SessionSettings{RetentionDays: 30}
	s.CleanupInterval.Value = 24
	s.CleanupInterval.Unit = "hour"
	return s
}
