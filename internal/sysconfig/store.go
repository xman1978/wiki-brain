package sysconfig

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	keyFileView       = "fileview"
	keySession        = "session"
	keyHelpManualHash = "help_manual_hash"
)

var ErrInvalidInput = errors.New("sysconfig: invalid input")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) GetFileView() (FileViewSettings, error) {
	var v FileViewSettings
	ok, err := s.get(keyFileView, &v)
	if err != nil {
		return FileViewSettings{}, err
	}
	if !ok {
		return DefaultFileViewSettings(), nil
	}
	return v, nil
}

func (s *Store) SetFileView(v FileViewSettings) error {
	return s.set(keyFileView, v)
}

func (s *Store) GetSession() (SessionSettings, error) {
	var v SessionSettings
	ok, err := s.get(keySession, &v)
	if err != nil {
		return SessionSettings{}, err
	}
	if !ok {
		return DefaultSessionSettings(), nil
	}
	return v, nil
}

func (s *Store) SetSession(v SessionSettings) error {
	return s.set(keySession, v)
}

// GetHelpManualHash returns the content hash of the built-in 使用手册
// (web/manual.md) that was last successfully imported/synced as a Source,
// or "" if it has never been synced yet.
func (s *Store) GetHelpManualHash() (string, error) {
	var v string
	ok, err := s.get(keyHelpManualHash, &v)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return v, nil
}

func (s *Store) SetHelpManualHash(hash string) error {
	return s.set(keyHelpManualHash, hash)
}

func (s *Store) get(key string, out any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("sysconfig: decode %s: %w", key, err)
	}
	return true, nil
}

func (s *Store) set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, string(raw))
	return err
}

// Duration converts CleanupInterval into a time.Duration, defaulting to
// 24h for an unrecognized or zero unit/value.
func (s SessionSettings) Duration() time.Duration {
	if s.CleanupInterval.Value <= 0 {
		return 24 * time.Hour
	}
	switch s.CleanupInterval.Unit {
	case "day":
		return time.Duration(s.CleanupInterval.Value) * 24 * time.Hour
	default:
		return time.Duration(s.CleanupInterval.Value) * time.Hour
	}
}

func ValidateFileView(v *FileViewSettings) error {
	if v.UseRemote {
		if v.BaseURL == "" {
			return fmt.Errorf("%w: base_url required when use_remote", ErrInvalidInput)
		}
		if v.TimeoutSeconds <= 0 {
			v.TimeoutSeconds = 600
		}
		if v.PollIntervalMs <= 0 {
			v.PollIntervalMs = 1500
		}
		// OCR only applies to the local-conversion fallback (远程转换服务与
		// OCR 互斥，见页面文件转换服务设置区域) — remote mode silently wins.
		v.OCREnabled = false
	}
	if v.OCREnabled && v.OCRMaxPages <= 0 {
		v.OCRMaxPages = 50
	}
	return nil
}

func ValidateSession(v *SessionSettings) error {
	if v.RetentionDays <= 0 {
		return fmt.Errorf("%w: retention_days must be positive", ErrInvalidInput)
	}
	if v.CleanupInterval.Value <= 0 {
		return fmt.Errorf("%w: cleanup_interval.value must be positive", ErrInvalidInput)
	}
	switch v.CleanupInterval.Unit {
	case "hour", "day":
	default:
		return fmt.Errorf("%w: cleanup_interval.unit must be hour or day", ErrInvalidInput)
	}
	return nil
}
