package unit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func equalRanges(a, b [][2]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGapRanges(t *testing.T) {
	cases := []struct {
		name             string
		segStart, segEnd int
		covered          [][2]int
		want             [][2]int
	}{
		{"no gaps", 1, 5, [][2]int{{1, 5}}, nil},
		{"gap at start", 3, 8, [][2]int{{5, 8}}, [][2]int{{3, 4}}},
		{"gap at end", 3, 8, [][2]int{{3, 5}}, [][2]int{{6, 8}}},
		{"gap in middle", 1, 10, [][2]int{{1, 3}, {7, 10}}, [][2]int{{4, 6}}},
		{"no coverage at all", 1, 3, nil, [][2]int{{1, 3}}},
		{"multiple gaps", 1, 12, [][2]int{{2, 3}, {8, 9}}, [][2]int{{1, 1}, {4, 7}, {10, 12}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gapRanges(c.segStart, c.segEnd, c.covered)
			if !equalRanges(got, c.want) {
				t.Errorf("gapRanges(%d,%d,%v) = %v, want %v", c.segStart, c.segEnd, c.covered, got, c.want)
			}
		})
	}
}

func TestIsTrivialGap(t *testing.T) {
	mdLines := []string{
		"# 标题",          // 1
		"",              // 2
		"## 子标题",        // 3
		"| --- | --- |", // 4
		"实质内容。",         // 5
		"---",           // 6
	}
	cases := []struct {
		name       string
		start, end int
		want       bool
	}{
		{"heading + blank", 1, 2, true},
		{"heading + separator", 3, 4, true},
		{"includes real content", 4, 5, false},
		{"horizontal rule alone", 6, 6, true},
		{"single heading", 3, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isTrivialGap(mdLines, c.start, c.end)
			if got != c.want {
				t.Errorf("isTrivialGap(%d,%d) = %v, want %v", c.start, c.end, got, c.want)
			}
		})
	}
}

func TestGapContextText(t *testing.T) {
	mdLines := []string{
		"IF v_mem_mb >= 16000 THEN", // 1
		"MEMORY_POOL := 2048;",      // 2
		"CACHE_POOL_SIZE := 1024;",  // 3
		"SORT_FLAG = 0;",            // 4
		"END IF;",                   // 5
	}
	seg := Segment{LineStart: 1, LineEnd: 5}

	t.Run("context on both sides", func(t *testing.T) {
		got := gapContextText(mdLines, seg, 3, 3)
		want := "[以下第 1-2 行是上下文，仅供理解，不要在这些行上生成 unit]\n" +
			"[1] IF v_mem_mb >= 16000 THEN\n[2] MEMORY_POOL := 2048;\n" +
			"[以下第 3-3 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n" +
			"[3] CACHE_POOL_SIZE := 1024;\n" +
			"[以下第 4-5 行是上下文，仅供理解，不要在这些行上生成 unit]\n" +
			"[4] SORT_FLAG = 0;\n[5] END IF;\n"
		if got != want {
			t.Errorf("gapContextText =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("gap at segment start has no leading context", func(t *testing.T) {
		got := gapContextText(mdLines, seg, 1, 2)
		if want := "[以下第 1-2 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n[1] IF v_mem_mb >= 16000 THEN\n[2] MEMORY_POOL := 2048;\n[以下第 3-5 行是上下文，仅供理解，不要在这些行上生成 unit]\n[3] CACHE_POOL_SIZE := 1024;\n[4] SORT_FLAG = 0;\n[5] END IF;\n"; got != want {
			t.Errorf("gapContextText =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("gap at segment end has no trailing context", func(t *testing.T) {
		got := gapContextText(mdLines, seg, 4, 5)
		if want := "[以下第 1-3 行是上下文，仅供理解，不要在这些行上生成 unit]\n[1] IF v_mem_mb >= 16000 THEN\n[2] MEMORY_POOL := 2048;\n[3] CACHE_POOL_SIZE := 1024;\n[以下第 4-5 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n[4] SORT_FLAG = 0;\n[5] END IF;\n"; got != want {
			t.Errorf("gapContextText =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("gap is the entire segment has no context either side", func(t *testing.T) {
		got := gapContextText(mdLines, seg, 1, 5)
		want := "[以下第 1-5 行是本次需要处理的目标行范围，只在这个范围内生成 unit]\n" + sliceLinesWithLineNumbers(mdLines, 1, 5)
		if got != want {
			t.Errorf("gapContextText =\n%q\nwant\n%q", got, want)
		}
	})
}

// TestExtract_GapFill_ModelAnchorsInContextZoneIsRejected is the safety-net
// regression test for gapContextText: even though the target-range framing
// tells the model not to emit units for the context lines, nothing enforces
// that beyond wording. If the model anchors a unit inside the context region
// anyway, LocateUnitBounds (scoped to gapSeg = gapStart..gapEnd) must fail to
// locate it — same as any other hallucinated anchor — so it never gets
// silently inserted as a duplicate of already-covered lines.
func TestExtract_GapFill_ModelAnchorsInContextZoneIsRejected(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeGapMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 4, 'structural', 1)`)

	mainResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "第一段", LineStart: 1, FirstLineAnchor: "# 标题", LineEnd: 2, LastLineAnchor: "第一段实质内容。"},
			{UnitID: "2", Center: "第三段", LineStart: 4, FirstLineAnchor: "第三段实质内容。", LineEnd: 4, LastLineAnchor: "第三段实质内容。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "第一段知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "第三段知识点", Type: "definition"},
		},
	}
	// The gap is line 3 alone; this response claims standalone but ignores
	// the "only line 3" instruction and anchors to line 2 (context, already
	// covered by unit 1) instead.
	gapJSON := `{"action": "standalone",
		"units": [{"unit_id": "1", "center": "误锚到上下文", "line_start": 2, "first_line_anchor": "第一段实质内容。", "line_end": 2, "last_line_anchor": "第一段实质内容。"}],
		"points": [{"point_id": "1", "unit_id": "1", "content": "不应该被写入的知识点", "type": "definition"}]}`

	setSplitExtractFakes(t, fake, mainResp)
	fake.SetResponse("unit_gap_extract.md", llm.FakeResponse{Output: gapJSON})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (context-zone anchor must be rejected, not inserted as a 3rd unit)", len(units))
	}
	for _, u := range units {
		if u.Center == "误锚到上下文" {
			t.Fatalf("a unit anchored inside the context zone was inserted: %+v", u)
		}
	}

	// Rejected, so the gap should have fallen back to merging into its
	// nearest neighbor instead of being silently dropped.
	var first *KnowledgeUnit
	for i := range units {
		if units[i].Center == "第一段" {
			first = &units[i]
		}
	}
	if first == nil {
		t.Fatal("first unit not found")
	}
	if first.LineStart != 1 || first.LineEnd != 3 {
		t.Errorf("first unit range = %d-%d, want 1-3 (gap merged into neighbor after rejection)", first.LineStart, first.LineEnd)
	}
}

// writeGapMarkdown builds a small doc where the first extraction pass will
// plausibly leave line-3 content (a standalone sentence) uncovered between
// two sibling units, so the gap-fill scenarios below have exactly one
// resolvable gap to work with.
func writeGapMarkdown(t *testing.T, dir string) string {
	t.Helper()
	mdPath := filepath.Join(dir, "test.md")
	content := "# 标题\n第一段实质内容。\n第二段完全独立的实质内容，模型第一次没有提取到。\n第三段实质内容。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	return mdPath
}

func TestExtract_GapFill_TrivialGapMergesIntoNeighborNoExtraCall(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := filepath.Join(tmpDir, "test.md")
	// Lines: 1 "## 前置小标题" / 2 "第一段实质内容。" / 3 "" / 4 "第二段实质内容。" /
	// 5 "" / 6 "## 末尾小标题占位" — every gap is pure heading/blank scaffolding.
	content := "## 前置小标题\n第一段实质内容。\n\n第二段实质内容。\n\n## 末尾小标题占位"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 6, 'structural', 1)`)

	extractResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "第一段", LineStart: 2, FirstLineAnchor: "第一段实质内容。", LineEnd: 2, LastLineAnchor: "第一段实质内容。"},
			{UnitID: "2", Center: "第二段", LineStart: 4, FirstLineAnchor: "第二段实质内容。", LineEnd: 4, LastLineAnchor: "第二段实质内容。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "第一段知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "第二段知识点", Type: "definition"},
		},
	}
	setSplitExtractFakes(t, fake, extractResp)
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (no new unit from a trivial gap)", len(units))
	}

	byCenter := map[string]KnowledgeUnit{}
	for _, u := range units {
		byCenter[u.Center] = u
	}
	first, second := byCenter["第一段"], byCenter["第二段"]
	if first.LineStart != 1 || first.LineEnd != 3 {
		t.Errorf("first unit range = %d-%d, want 1-3 (absorbed leading heading, and the sandwiched blank on a tie broken to the earlier neighbor)", first.LineStart, first.LineEnd)
	}
	if second.LineStart != 4 || second.LineEnd != 6 {
		t.Errorf("second unit range = %d-%d, want 4-6 (absorbed its own trailing heading, strictly closer than the first unit)", second.LineStart, second.LineEnd)
	}

	calls := 0
	for _, c := range fake.Calls() {
		if c.PromptFile == "unit_boundary_extract.md" {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("unit_boundary_extract.md called %d times, want 1 (trivial gaps must not trigger re-extraction)", calls)
	}
}

func TestExtract_GapFill_SubstantiveGapGetsOwnUnit(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeGapMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 4, 'structural', 1)`)

	mainResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "第一段", LineStart: 1, FirstLineAnchor: "# 标题", LineEnd: 2, LastLineAnchor: "第一段实质内容。"},
			{UnitID: "2", Center: "第三段", LineStart: 4, FirstLineAnchor: "第三段实质内容。", LineEnd: 4, LastLineAnchor: "第三段实质内容。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "第一段知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "第三段知识点", Type: "definition"},
		},
	}
	gapJSON := `{"action": "standalone",
		"units": [{"unit_id": "1", "center": "被漏掉的第二段", "line_start": 3, "first_line_anchor": "第二段完全独立的实质内容，模型第一次没有提取到。", "line_end": 3, "last_line_anchor": "第二段完全独立的实质内容，模型第一次没有提取到。"}],
		"points": [{"point_id": "1", "unit_id": "1", "content": "补提取到的知识点", "type": "definition"}]}`

	setSplitExtractFakes(t, fake, mainResp)
	fake.SetResponse("unit_gap_extract.md", llm.FakeResponse{Output: gapJSON})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3 (gap must become its own unit)", len(units))
	}

	var filled *KnowledgeUnit
	for i := range units {
		if units[i].Center == "被漏掉的第二段" {
			filled = &units[i]
		}
	}
	if filled == nil {
		t.Fatal("gap-filled unit not found")
	}
	if filled.LineStart != 3 || filled.LineEnd != 3 {
		t.Errorf("gap-filled unit range = %d-%d, want 3-3", filled.LineStart, filled.LineEnd)
	}

	points, _ := svc.store.GetPointsByUnitID(filled.UnitID)
	if len(points) != 1 || points[0].Content != "补提取到的知识点" {
		t.Errorf("gap-filled unit points = %+v, want one point 补提取到的知识点", points)
	}

	extractCalls, gapCalls := 0, 0
	for _, c := range fake.Calls() {
		switch c.PromptFile {
		case "unit_boundary_extract.md":
			extractCalls++
		case "unit_gap_extract.md":
			gapCalls++
		}
	}
	if extractCalls != 1 || gapCalls != 1 {
		t.Errorf("unit_boundary_extract.md called %d times (want 1), unit_gap_extract.md called %d times (want 1)", extractCalls, gapCalls)
	}
}

func TestExtract_GapFill_ModelDeclinesGapMergesIntoNeighbor(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeGapMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 4, 'structural', 1)`)

	mainResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "第一段", LineStart: 1, FirstLineAnchor: "# 标题", LineEnd: 2, LastLineAnchor: "第一段实质内容。"},
			{UnitID: "2", Center: "第三段", LineStart: 4, FirstLineAnchor: "第三段实质内容。", LineEnd: 4, LastLineAnchor: "第三段实质内容。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "第一段知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "第三段知识点", Type: "definition"},
		},
	}
	setSplitExtractFakes(t, fake, mainResp)
	// model judges the gap metadata/decoration, not worth its own unit
	fake.SetResponse("unit_gap_extract.md", llm.FakeResponse{Output: `{"action": "skip"}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (declined gap must merge, not become a unit)", len(units))
	}

	var first *KnowledgeUnit
	for i := range units {
		if units[i].Center == "第一段" {
			first = &units[i]
		}
	}
	if first == nil {
		t.Fatal("first unit not found")
	}
	if first.LineStart != 1 || first.LineEnd != 3 {
		t.Errorf("first unit range = %d-%d, want 1-3 (absorbed the declined gap, tie broken to the earlier neighbor)", first.LineStart, first.LineEnd)
	}
}
