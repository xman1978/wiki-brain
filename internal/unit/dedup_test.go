package unit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// writeDedupMarkdown builds a minimal two-line doc: two adjacent
// (zero-gap) single-line units after the main extraction pass, so
// dedupCandidates has exactly one candidate pair to judge and gap-fill has
// nothing to do (no gaps between or around them).
func writeDedupMarkdown(t *testing.T, dir string) string {
	t.Helper()
	mdPath := filepath.Join(dir, "test.md")
	content := "第一部分内容简述。\n第二部分内容详述，其实是同一件事。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	return mdPath
}

func mainExtractRespTwoAdjacentUnits() extractOutput {
	return extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "主题概述", LineStart: 1, FirstLineAnchor: "第一部分内容简述。", LineEnd: 1, LastLineAnchor: "第一部分内容简述。"},
			{UnitID: "2", Center: "主题详述", LineStart: 2, FirstLineAnchor: "第二部分内容详述，其实是同一件事。", LineEnd: 2, LastLineAnchor: "第二部分内容详述，其实是同一件事。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "简述版知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "详述版知识点", Type: "definition"},
		},
	}
}

// TestDedupCandidates_GapThreshold exercises dedupCandidates directly
// (bypassing fillGaps, which would otherwise close a small trivial gap
// between two units before dedup ever saw them) to pin the exact boundary
// of dedupMaxGapLines: real duplicates found in practice sat 1-3 lines
// apart, never touching (see docs/impl/mvp/unit.md 3.3), so a pair exactly
// at the threshold must still be checked, and one line further must not.
func TestDedupCandidates_GapThreshold(t *testing.T) {
	mdLines := []string{
		"第一次表述同一件事。", "", "", "", "第二次表述同一件事，只是换了说法。",
	}
	seg := Segment{LineStart: 1, LineEnd: 5}

	t.Run("within threshold gets checked and merged", func(t *testing.T) {
		svc, fake, db := setupTestService(t)
		insertSource(t, db, "src-1", "/tmp/unused.md")

		fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
		fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "合并后的主题", "points": [{"content": "合并去重后的知识点内容", "type": "rule"}]}`})

		candidates := []unitCandidate{
			{id: "u1", llm: llmUnit{UnitID: "1", Center: "主题概述"}, points: []llmPoint{{UnitID: "1", Content: "简述版知识点", Type: "definition"}}, lineStart: 1, lineEnd: 1, seg: seg},
			{id: "u2", llm: llmUnit{UnitID: "2", Center: "主题详述"}, points: []llmPoint{{UnitID: "2", Content: "详述版知识点", Type: "definition"}}, lineStart: 5, lineEnd: 5, seg: seg},
		}
		// gap = 5 - 1 - 1 = 3 = dedupMaxGapLines, right at the boundary.
		got := svc.dedupCandidates(t.Context(), "src-1", mdLines, candidates)

		if len(got) != 1 {
			t.Fatalf("got %d candidates, want 1 (gap of exactly dedupMaxGapLines must still be checked)", len(got))
		}

		calls := 0
		for _, c := range fake.Calls() {
			if c.PromptFile == "unit_dedup_classify.md" {
				calls++
			}
		}
		if calls != 1 {
			t.Errorf("unit_dedup_classify.md called %d times, want 1", calls)
		}
	})

	t.Run("beyond threshold is never checked", func(t *testing.T) {
		svc, fake, db := setupTestService(t)
		insertSource(t, db, "src-1", "/tmp/unused.md")

		// If dedup mistakenly checked this pair anyway, it would report a
		// merge — configuring it to always say "duplicate" makes the
		// assertion below a real test of the gap filter, not a no-op.
		fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
		fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "不应该被调用", "points": [{"content": "不应该被调用", "type": "rule"}]}`})

		seg := Segment{LineStart: 1, LineEnd: 6}
		candidates := []unitCandidate{
			{id: "u1", llm: llmUnit{UnitID: "1", Center: "主题概述"}, points: []llmPoint{{UnitID: "1", Content: "简述版知识点", Type: "definition"}}, lineStart: 1, lineEnd: 1, seg: seg},
			{id: "u2", llm: llmUnit{UnitID: "2", Center: "主题详述"}, points: []llmPoint{{UnitID: "2", Content: "详述版知识点", Type: "definition"}}, lineStart: 6, lineEnd: 6, seg: seg},
		}
		// gap = 6 - 1 - 1 = 4 = dedupMaxGapLines + 1, one past the boundary.
		got := svc.dedupCandidates(t.Context(), "src-1", mdLines, candidates)

		if len(got) != 2 {
			t.Fatalf("got %d candidates, want 2 (gap beyond dedupMaxGapLines must not be checked)", len(got))
		}
		calls := 0
		for _, c := range fake.Calls() {
			if c.PromptFile == "unit_dedup_classify.md" {
				calls++
			}
		}
		if calls != 0 {
			t.Errorf("unit_dedup_classify.md called %d times, want 0 (pair is beyond the gap threshold)", calls)
		}
	})
}

func TestExtract_Dedup_MergesTrueDuplicate(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeDedupMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	setSplitExtractFakes(t, fake, mainExtractRespTwoAdjacentUnits())
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "合并后的主题", "points": [{"content": "合并去重后的知识点内容", "type": "rule"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1 (duplicate pair must collapse into one)", len(units))
	}
	u := units[0]
	if u.Center != "合并后的主题" {
		t.Errorf("center = %q, want 合并后的主题", u.Center)
	}
	if u.LineStart != 1 || u.LineEnd != 2 {
		t.Errorf("range = %d-%d, want 1-2 (union of both originals)", u.LineStart, u.LineEnd)
	}

	points, _ := svc.store.GetPointsByUnitID(u.UnitID)
	if len(points) != 1 || points[0].Content != "合并去重后的知识点内容" {
		t.Errorf("points = %+v, want exactly one 合并去重后的知识点内容", points)
	}
}

func TestExtract_Dedup_KeepsSeparateWhenNotDuplicate(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeDedupMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	setSplitExtractFakes(t, fake, mainExtractRespTwoAdjacentUnits())
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "parallel", "reason": "同一主题下的不同规则"}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (LLM said not duplicate, both must survive untouched)", len(units))
	}
	byCenter := map[string]KnowledgeUnit{}
	for _, u := range units {
		byCenter[u.Center] = u
	}
	if _, ok := byCenter["主题概述"]; !ok {
		t.Error("主题概述 unit missing")
	}
	if _, ok := byCenter["主题详述"]; !ok {
		t.Error("主题详述 unit missing")
	}
}

func TestExtract_Dedup_CollapsesChainOfThree(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := filepath.Join(tmpDir, "test.md")
	content := "第一次表述同一件事。\n第二次表述同一件事。\n第三次表述同一件事。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 3, 'structural', 1)`)

	extractResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "重复一", LineStart: 1, FirstLineAnchor: "第一次表述同一件事。", LineEnd: 1, LastLineAnchor: "第一次表述同一件事。"},
			{UnitID: "2", Center: "重复二", LineStart: 2, FirstLineAnchor: "第二次表述同一件事。", LineEnd: 2, LastLineAnchor: "第二次表述同一件事。"},
			{UnitID: "3", Center: "重复三", LineStart: 3, FirstLineAnchor: "第三次表述同一件事。", LineEnd: 3, LastLineAnchor: "第三次表述同一件事。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "知识点一", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "知识点二", Type: "definition"},
			{PointID: "3", UnitID: "3", Content: "知识点三", Type: "definition"},
		},
	}
	setSplitExtractFakes(t, fake, extractResp)
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "同一件事", "points": [{"content": "去重后只剩一条知识点", "type": "rule"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1 (three-way duplicate chain must fully collapse)", len(units))
	}
	if units[0].LineStart != 1 || units[0].LineEnd != 3 {
		t.Errorf("range = %d-%d, want 1-3", units[0].LineStart, units[0].LineEnd)
	}
}

// TestDeterministicMerge pins the no-LLM fast path: identical ranges plus
// identical normalized centers merge programmatically, anything else defers
// to the model.
func TestDeterministicMerge(t *testing.T) {
	pts := func(contents ...string) []llmDedupPoint {
		var out []llmDedupPoint
		for _, c := range contents {
			out = append(out, llmDedupPoint{Content: c, Type: "rule"})
		}
		return out
	}

	t.Run("same range same normalized center merges", func(t *testing.T) {
		m := deterministicMerge("用户权限申请（含审批）", "用户权限申请", 5, 10, 5, 10,
			pts("申请需要部门审批", "审批时限为三个工作日"), pts("申请需要部门审批。", "超时自动驳回"))
		if m == nil {
			t.Fatal("want deterministic merge, got nil")
		}
		if m.Center != "用户权限申请（含审批）" {
			t.Errorf("center = %q, want the longer original", m.Center)
		}
		// 申请需要部门审批 dedupes (keeping the longer variant); the other two survive.
		if len(m.Points) != 3 {
			t.Errorf("points = %+v, want 3 (one pair deduped, unique ones kept)", m.Points)
		}
	})

	t.Run("different range does not qualify", func(t *testing.T) {
		if m := deterministicMerge("同一主题", "同一主题", 5, 10, 5, 11, pts("a"), pts("b")); m != nil {
			t.Errorf("want nil for different ranges, got %+v", m)
		}
	})

	t.Run("different center does not qualify", func(t *testing.T) {
		if m := deterministicMerge("服务启动流程", "服务停止流程", 5, 10, 5, 10, pts("a"), pts("b")); m != nil {
			t.Errorf("want nil for different centers, got %+v", m)
		}
	})
}

// TestExtract_Dedup_ParentChildKeepsBoth verifies the classify/merge split:
// when unit_dedup_classify.md answers parent_child, both units survive and
// unit_dedup_merge.md is never consulted.
func TestExtract_Dedup_ParentChildKeepsBoth(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeDedupMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	setSplitExtractFakes(t, fake, mainExtractRespTwoAdjacentUnits())
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "parent_child", "reason": "一个是总览另一个是细节"}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (parent_child must keep both)", len(units))
	}
	for _, c := range fake.Calls() {
		if c.PromptFile == "unit_dedup_merge.md" {
			t.Fatal("unit_dedup_merge.md must not be called when classify says parent_child")
		}
	}
}
