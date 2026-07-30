package session

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

func bootstrapModel(t *testing.T, cfg *config.Config) string {
	t.Helper()
	if cfg.BootstrapLLM == nil || cfg.BootstrapLLM.Models == nil {
		return "(none)"
	}
	if m, ok := cfg.BootstrapLLM.Models["default"]; ok {
		return m.Model
	}
	return "(none)"
}
