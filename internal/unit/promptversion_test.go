package unit

import (
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// TestPromptVersionConstantsMatchTemplates pins the promptVersion* constants
// to the `version:` frontmatter of the templates they describe. The two
// drifted once — unit_extract_retry.md moved to v6 while the code kept
// stamping "v5", making duplicate twins look like different extraction
// generations — and nothing but this test prevents a repeat.
func TestPromptVersionConstantsMatchTemplates(t *testing.T) {
	cases := []struct {
		promptFile string
		constant   string
	}{
		{"unit_extract_retry.md", promptVersionExtractRetry},
		{"unit_gap_extract.md", promptVersionGapExtract},
		{"unit_boundary_extract.md", promptVersionBoundaryExtract},
		{"unit_point_extract.md", promptVersionPointExtract},
		{"kpn_extract.md", promptVersionKPNExtract},
		{"kpn_cross_match.md", promptVersionKPNCross},
	}
	for _, c := range cases {
		t.Run(c.promptFile, func(t *testing.T) {
			p, err := llm.LoadPrompt(filepath.Join("..", "..", "config", "prompts", c.promptFile), nil)
			if err != nil {
				t.Fatalf("load template: %v", err)
			}
			if p.Version != c.constant {
				t.Errorf("template %s frontmatter version = %q, but code stamps %q — update the constant (or the template) so stored prompt_version stays truthful",
					c.promptFile, p.Version, c.constant)
			}
		})
	}
}
