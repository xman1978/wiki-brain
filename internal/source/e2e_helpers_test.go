//go:build integration

package source

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func mustExtractionModel(t *testing.T, pm llm.PurposeModels) llm.ModelParams {
	t.Helper()
	mc, err := pm.ModelForPurpose("extraction")
	if err != nil {
		t.Fatal(err)
	}
	return mc
}
