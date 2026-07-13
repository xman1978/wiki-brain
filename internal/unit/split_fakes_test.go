package unit

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// setSplitExtractFakes converts a legacy-shaped extractOutput fixture into
// the split pipeline's fake responses: one unit_boundary_extract.md response
// whose units list the fixture units' line ranges, plus one queued
// unit_point_extract.md response per unit (in the same order) echoing the
// unit's center and its points. Line numbers drive everything —
// buildLLMUnitFromBoundary owns the canonical text, so the content strings
// only need valid "[n]" prefixes.
func setSplitExtractFakes(t *testing.T, fake *llm.FakeClient, out extractOutput) {
	t.Helper()
	fake.SetResponse("unit_boundary_extract.md", llm.FakeResponse{Output: splitBoundaryResp(out)})
	fake.SetResponseSequence("unit_point_extract.md", splitPointResps(out))
}

func splitBoundaryResp(out extractOutput) string {
	type boundaryUnitJSON struct {
		Center  string   `json:"center"`
		Content []string `json:"content"`
	}
	units := make([]boundaryUnitJSON, 0, len(out.Units))
	for _, u := range out.Units {
		bu := boundaryUnitJSON{Center: u.Center}
		for line := u.LineStart; line <= u.LineEnd; line++ {
			bu.Content = append(bu.Content, fmt.Sprintf("[%d] %s", line, u.FirstLineAnchor))
		}
		units = append(units, bu)
	}
	b, _ := json.Marshal(map[string]any{"units": units})
	return string(b)
}

func splitPointResps(out extractOutput) []llm.FakeResponse {
	type pointJSON struct {
		Content string `json:"content"`
		Type    string `json:"type"`
	}
	resps := make([]llm.FakeResponse, 0, len(out.Units))
	for _, u := range out.Units {
		var pts []pointJSON
		for _, p := range out.Points {
			if p.UnitID == u.UnitID {
				pts = append(pts, pointJSON{Content: p.Content, Type: p.Type})
			}
		}
		b, _ := json.Marshal(map[string]any{"center": u.Center, "points": pts})
		resps = append(resps, llm.FakeResponse{Output: string(b)})
	}
	return resps
}
