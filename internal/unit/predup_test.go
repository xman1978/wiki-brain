package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
)

// setupPreInsertDedupTestService uses the only supported extraction path:
// concurrent segment extraction followed by in-memory gap-fill/dedup.
// concurrency=0 falls back to preInsertDedupConcurrencyDefault; tests that
// need deterministic call ordering (e.g. SetResponseSequence) should pass 1.
func setupPreInsertDedupTestService(t *testing.T, minOverlap float64, concurrency int) (*Service, *llm.FakeClient, *sql.DB) {
	t.Helper()
	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()

	sourceStore := source.NewStore(db)
	unitStore := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(100)

	idxDir := filepath.Join(tmpDir, "index")
	idxMgr, err := index.NewManager(idxDir)
	if err != nil {
		t.Fatalf("create index: %v", err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	cfg := &config.Config{
		Source: config.SourceConfig{
			SegmentMaxChars:           4000,
			MinSegmentChars:           400,
			PreInsertDedupMinOverlap:  minOverlap,
			PreInsertDedupConcurrency: concurrency,
		},
	}

	svc := NewService(unitStore, sourceStore, fake, idxMgr.Units, idxMgr.Points, q, cfg)
	return svc, fake, db
}

func countCalls(fake *llm.FakeClient, promptFile string) int {
	n := 0
	for _, c := range fake.Calls() {
		if c.PromptFile == promptFile {
			n++
		}
	}
	return n
}

func TestExtractRerankSemanticsBatchesByFinalTextAndRunsConcurrently(t *testing.T) {
	svc, tracker := setupRerankSemanticExtractionService(t, 1, 2)
	tracker.releaseAfterStarts(2)

	pool := []unitCandidate{
		{id: "u1", lineStart: 1, lineEnd: 1},
		{id: "u2", lineStart: 2, lineEnd: 2},
		{id: "u3", lineStart: 3, lineEnd: 3},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := svc.extractRerankSemantics(ctx, "差旅制度", []string{"甲", "乙", "丙"}, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d semantics, want 3", len(got))
	}
	if tracker.MaxConcurrent() < 2 {
		t.Fatalf("max concurrency = %d, want >= 2", tracker.MaxConcurrent())
	}
	for _, id := range []string{"u1", "u2", "u3"} {
		if got[id].PromptVersion != rerank.ExtractPromptVersion {
			t.Fatalf("%s prompt version = %q, want %q", id, got[id].PromptVersion, rerank.ExtractPromptVersion)
		}
	}
}

func TestExtractRerankSemanticsRejectsInvalidResultCoverage(t *testing.T) {
	pool := []unitCandidate{
		{id: "u1", lineStart: 1, lineEnd: 1},
		{id: "u2", lineStart: 2, lineEnd: 2},
	}

	for _, tc := range []struct {
		name      string
		resultIDs []string
	}{
		{name: "unknown ID", resultIDs: []string{"u1", "unknown"}},
		{name: "duplicate ID", resultIDs: []string{"u1", "u1"}},
		{name: "omitted ID", resultIDs: []string{"u1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tracker := setupRerankSemanticExtractionService(t, 4000, 1)
			tracker.setResultIDs(tc.resultIDs)
			tracker.release()

			_, err := svc.extractRerankSemantics(t.Context(), "差旅制度", []string{"甲", "乙"}, pool)
			if err == nil {
				t.Fatal("extractRerankSemantics returned nil error")
			}
		})
	}
}

func setupRerankSemanticExtractionService(t *testing.T, batchMaxChars, concurrency int) (*Service, *rerankSemanticExtractionTracker) {
	t.Helper()
	svc, _, _ := setupPreInsertDedupTestService(t, 0.1, 1)
	tracker := newRerankSemanticExtractionTracker()
	svc.llmClient = tracker
	svc.cfg.Retrieval.RerankExtractBatchMaxChars = batchMaxChars
	svc.cfg.Retrieval.RerankExtractConcurrency = concurrency
	return svc, tracker
}

type rerankSemanticExtractionTracker struct {
	mu          sync.Mutex
	releaseCh   chan struct{}
	startedCh   chan struct{}
	releaseOnce sync.Once
	resultIDs   []string
	inFlight    int
	maxInFlight int
}

func newRerankSemanticExtractionTracker() *rerankSemanticExtractionTracker {
	return &rerankSemanticExtractionTracker{
		releaseCh: make(chan struct{}),
		startedCh: make(chan struct{}, 8),
	}
}

func (f *rerankSemanticExtractionTracker) Complete(_ context.Context, promptFile string, vars map[string]string, model string) (string, error) {
	return "", errors.New("unexpected Complete call: " + promptFile)
}

func (f *rerankSemanticExtractionTracker) CompleteStream(_ context.Context, promptFile string, vars map[string]string, model string) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("unexpected CompleteStream call: " + promptFile)
}

func (f *rerankSemanticExtractionTracker) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	if promptFile != "unit_semantics_extract.md" {
		return nil, errors.New("unexpected prompt: " + promptFile)
	}
	ids := unitIDsFromPrompt(vars["units"])

	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	resultIDs := append([]string(nil), f.resultIDs...)
	f.mu.Unlock()
	f.startedCh <- struct{}{}
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.releaseCh:
	}
	if resultIDs == nil {
		resultIDs = ids
	}

	results := make([]map[string]interface{}, 0, len(resultIDs))
	for _, id := range resultIDs {
		results = append(results, map[string]interface{}{
			"unit_id": id, "source_theme": "theme", "content_theme": "content",
			"intent": "说明", "object": "object", "scope": "通用", "key_facts": []string{"fact"},
		})
	}
	return json.Marshal(map[string]interface{}{"results": results})
}

func (f *rerankSemanticExtractionTracker) releaseAfterStarts(n int) {
	go func() {
		for range n {
			<-f.startedCh
		}
		f.release()
	}()
}

func (f *rerankSemanticExtractionTracker) release() {
	f.releaseOnce.Do(func() { close(f.releaseCh) })
}

func (f *rerankSemanticExtractionTracker) setResultIDs(ids []string) {
	f.mu.Lock()
	f.resultIDs = append([]string(nil), ids...)
	f.mu.Unlock()
}

func (f *rerankSemanticExtractionTracker) MaxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight
}

var unitIDFromPromptPattern = regexp.MustCompile(`\[([^\]]+)\]`)

func unitIDsFromPrompt(units string) []string {
	matches := unitIDFromPromptPattern.FindAllStringSubmatch(units, -1)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match[1])
	}
	return ids
}

func TestExtractSegmentOutputSplit_AssemblesLegacyOutput(t *testing.T) {
	svc, fake, _ := setupPreInsertDedupTestService(t, 0.1, 1)

	mdLines := []string{
		"# 第八章 加班调休",
		"第三十条 加班",
		"员工加班应提前提交加班申请，经审批后方可执行。",
		"第三十一条 调休",
		"加班人员可以按照规定申请调休。",
	}
	seg := Segment{Title: "第八章 加班调休", LineStart: 1, LineEnd: 5}

	fake.SetResponse("unit_boundary_extract.md", llm.FakeResponse{Output: `{
		"units": [
			{"center": "加班申请与工时计算", "content": ["[1] # 第八章 加班调休", "[2] 第三十条 加班", "[3] 员工加班应提前提交加班申请，经审批后方可执行。"]},
			{"center": "调休申请规则", "content": ["[4] 第三十一条 调休", "[5] 加班人员可以按照规定申请调休。"]}
		]
	}`})
	fake.SetResponseSequence("unit_point_extract.md", []llm.FakeResponse{
		{Output: `{"center":"加班申请审批规则","points":[{"content":"员工加班需要提前提交申请并经过审批。","type":"rule"}]}`},
		{Output: `{"center":"调休申请规则","points":[{"content":"加班人员可以按规定申请调休。","type":"rule"}]}`},
	})

	output, ok := svc.extractSegmentOutputSplit(t.Context(), "src-split", seg, mdLines)
	if !ok {
		t.Fatalf("split extraction returned ok=false")
	}
	if got, want := len(output.Units), 2; got != want {
		t.Fatalf("units len = %d, want %d", got, want)
	}
	if got, want := len(output.Points), 2; got != want {
		t.Fatalf("points len = %d, want %d", got, want)
	}
	if output.Units[0].UnitID != "u1" || output.Points[0].UnitID != "u1" {
		t.Fatalf("generated local IDs not wired: unit=%q point.unit=%q", output.Units[0].UnitID, output.Points[0].UnitID)
	}
	if output.Units[0].LineStart != 1 || output.Units[0].LineEnd != 3 {
		t.Fatalf("first unit bounds = %d-%d, want 1-3", output.Units[0].LineStart, output.Units[0].LineEnd)
	}
	if output.Units[0].Center != "加班申请审批规则" {
		t.Fatalf("first unit center = %q, want point prompt center", output.Units[0].Center)
	}
	if countCalls(fake, "unit_boundary_extract.md") != 1 || countCalls(fake, "unit_point_extract.md") != 2 {
		t.Fatalf("unexpected prompt calls: boundary=%d point=%d", countCalls(fake, "unit_boundary_extract.md"), countCalls(fake, "unit_point_extract.md"))
	}
}

func TestBuildLLMUnitFromBoundaryFillsNonContiguousLines(t *testing.T) {
	mdLines := []string{"A", "B", "C"}
	seg := Segment{LineStart: 1, LineEnd: 3}
	u, content, lines, ok := buildLLMUnitFromBoundary(seg, mdLines, boundaryUnit{
		Center:  "跳行单元",
		Content: []boundaryLineItem{"[1] A", "[3] C"},
	}, 1)
	if !ok {
		t.Fatalf("non-contiguous boundary content should be repaired")
	}
	if u.LineStart != 1 || u.LineEnd != 3 {
		t.Fatalf("bounds = %d-%d, want 1-3", u.LineStart, u.LineEnd)
	}
	if got, want := lines, []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if !strings.Contains(content, "[2] B") {
		t.Fatalf("content was not rebuilt from original lines: %q", content)
	}
}

// TestExtract_PreInsertDedup_MergesBeforeInsert exercises the bypass on the
// same duplicate-pair fixture dedup_test.go uses for the post-insert path
// (a heading-like summary unit immediately followed by its own detail unit),
// confirming it collapses to one stored unit via the pre-insert route too.
func TestExtract_PreInsertDedup_MergesBeforeInsert(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.1, 1)
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
		t.Fatalf("got %d units, want 1 (duplicate pair must collapse into one before insert)", len(units))
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

	// Dedup only runs once now (pre-insert) — there's no post-insert backstop
	// left to double-check the same pair.
	if n := countCalls(fake, "unit_dedup_classify.md"); n != 1 {
		t.Errorf("unit_dedup_classify.md called %d times, want exactly 1 (pre-insert pass only)", n)
	}
}

// TestExtract_PreInsertDedup_SkipsLLMWhenNoOverlap sets the overlap
// threshold high enough that two adjacent, textually unrelated units never
// reach the LLM at all — the point of the bypass. Both lines carry 6+
// meaningful tokens each, comfortably above the short-lead-in threshold, so
// this exercises the overlap gate specifically rather than the short-token
// bypass added for TestExtract_PreInsertDedup_ShortNonChineseLeadInNotSkipped.
func TestExtract_PreInsertDedup_SkipsLLMWhenNoOverlap(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.9, 1)
	tmpDir := t.TempDir()

	mdPath := filepath.Join(tmpDir, "test.md")
	content := "苹果手机的电池续航时间比较长。\n今天天气晴朗适合户外运动锻炼。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	resp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "手机电池续航", LineStart: 1, FirstLineAnchor: "苹果手机的电池续航时间比较长。", LineEnd: 1, LastLineAnchor: "苹果手机的电池续航时间比较长。"},
			{UnitID: "2", Center: "户外运动天气", LineStart: 2, FirstLineAnchor: "今天天气晴朗适合户外运动锻炼。", LineEnd: 2, LastLineAnchor: "今天天气晴朗适合户外运动锻炼。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "苹果手机电池续航时间较长", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "晴天适合户外运动锻炼", Type: "definition"},
		},
	}
	setSplitExtractFakes(t, fake, resp)
	// If the overlap gate failed to skip this pair, this response would
	// force a false merge — configuring it to always say "duplicate" makes
	// the zero-calls assertion below a real test of the gate, not a no-op.
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "不应该被调用", "points": [{"content": "不应该被调用", "type": "rule"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (low overlap must skip the LLM call entirely, both units survive)", len(units))
	}
	if n := countCalls(fake, "unit_dedup_classify.md"); n != 0 {
		t.Errorf("unit_dedup_classify.md called %d times, want 0 (overlap below threshold must never call the LLM)", n)
	}
}

// TestExtract_PreInsertDedup_GapBecomesOwnUnit is the bypass counterpart of
// gapfill_test.go's TestExtract_GapFill_SubstantiveGapGetsOwnUnit: same
// fixture (a middle paragraph the main extraction call misses entirely), but
// resolved by fillGapsInMemory before any unit reaches the store instead of
// fillGaps updating already-inserted rows afterward.
func TestExtract_PreInsertDedup_GapBecomesOwnUnit(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.1, 1)
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

	if n := countCalls(fake, "unit_boundary_extract.md"); n != 1 {
		t.Errorf("unit_boundary_extract.md called %d times, want 1 (gap goes through unit_gap_extract.md now)", n)
	}
	if n := countCalls(fake, "unit_gap_extract.md"); n != 1 {
		t.Errorf("unit_gap_extract.md called %d times, want 1", n)
	}
}

// TestExtract_PreInsertDedup_ShortHeadingLongContentNotSkipped regression-
// tests a real failure found via test/markdown/培训积分管理办法.md: a
// one-line heading ("(二)年度积分基准线") immediately followed by its own
// long paragraph scored 0.09 Jaccard similarity — below the 0.15 default
// threshold — so dedupCandidates skipped the LLM call entirely and the
// heading survived as its own separate unit. tokenOverlap now uses a
// containment coefficient (shared / min(|A|,|B|) instead of shared / union)
// specifically so a short side's vocabulary being fully present in a long
// side's vocabulary still gates the call open (this pair scores 0.5
// containment). Uses the exact real text, not a paraphrase, so a future
// regression in the formula trips this test the same way it showed up in
// practice.
func TestExtract_PreInsertDedup_ShortHeadingLongContentNotSkipped(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0, 1) // 0 → real default threshold (0.15)
	tmpDir := t.TempDir()

	heading := "(二)年度积分基准线"
	content := "人力部门根据不同职能不同岗位的年度培训计划与安排在年度结束时公布各岗位的年度积分基准线。积分基本准线主要由出勤分+结果分构成。不同岗位的基准线不同。"

	mdPath := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(mdPath, []byte(heading+"\n\n"+content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 3, 'structural', 1)`)

	resp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "年度积分基准线的设定", LineStart: 1, FirstLineAnchor: heading, LineEnd: 1, LastLineAnchor: heading},
			{UnitID: "2", Center: "年度积分基准线的设定与构成", LineStart: 3, FirstLineAnchor: content, LineEnd: 3, LastLineAnchor: content},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "年度积分基准线由人力部门公布", Type: "rule"},
			{PointID: "2", UnitID: "2", Content: "基准线由出勤分+结果分构成，各岗位不同", Type: "rule"},
		},
	}
	setSplitExtractFakes(t, fake, resp)
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "年度积分基准线的设定与构成", "points": [{"content": "年度积分基准线由人力部门公布，由出勤分+结果分构成，各岗位不同", "type": "rule"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if n := countCalls(fake, "unit_dedup_classify.md"); n != 1 {
		t.Fatalf("unit_dedup_classify.md called %d times, want 1 (the overlap gate must not skip a short-heading/long-content pair)", n)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1 (heading and its own content must merge)", len(units))
	}
}

// TestExtract_PreInsertDedup_ShortNonChineseLeadInNotSkipped is a second
// regression case, also found via a real test/markdown file
// (神通数据库优化.md): a Chinese comment ("--杀掉数据库会话") immediately
// followed by the English/SQL command it introduces ("kill session sid
// abort;") shares zero gse tokens with it — Chinese word segmentation
// naturally finds nothing in common with Latin/code text, so the overlap
// score is 0 regardless of formula, not just low. dedupCandidates now skips
// the overlap gate entirely — always calling the LLM — whenever either
// side has at most PreInsertDedupShortTokenMax tokens, since a short
// lead-in's relationship to its content can't be judged by literal overlap
// at all, let alone reliably.
func TestExtract_PreInsertDedup_ShortNonChineseLeadInNotSkipped(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0, 1) // 0 → real default thresholds
	tmpDir := t.TempDir()

	comment := "--杀掉数据库会话"
	command := "kill session sid abort;"

	mdPath := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(mdPath, []byte(comment+"\n"+command), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	resp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "杀掉数据库会话以解除锁阻塞", LineStart: 1, FirstLineAnchor: comment, LineEnd: 1, LastLineAnchor: comment},
			{UnitID: "2", Center: "终止数据库阻塞会话的操作方法", LineStart: 2, FirstLineAnchor: command, LineEnd: 2, LastLineAnchor: command},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "杀掉数据库会话用于解除阻塞", Type: "method"},
			{PointID: "2", UnitID: "2", Content: "kill session sid abort 命令强制终止会话", Type: "method"},
		},
	}
	setSplitExtractFakes(t, fake, resp)
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "终止数据库阻塞会话的操作方法", "points": [{"content": "使用 kill session sid abort 命令杀掉数据库会话以解除阻塞", "type": "method"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if n := countCalls(fake, "unit_dedup_classify.md"); n != 1 {
		t.Fatalf("unit_dedup_classify.md called %d times, want 1 (a short lead-in must always be checked, regardless of token overlap)", n)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1 (comment and its own command must merge)", len(units))
	}
}

// TestExtract_PreInsertDedup_ConcurrentSegments exercises the worker pool
// with concurrency=2 across 3 segments. All three lines share identical text
// so one fake response (reported line number verified against whichever
// segment actually consumes it, falling back to its own anchor scan
// otherwise — see LocateUnitBounds) resolves correctly no matter which of
// the 2 workers a given segment lands on. The point is to prove segments
// running through concurrent producers and a single serial consumer don't
// drop, duplicate, or corrupt any segment's output.
func TestExtract_PreInsertDedup_ConcurrentSegments(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.1, 2)
	tmpDir := t.TempDir()

	mdPath := filepath.Join(tmpDir, "test.md")
	content := "独立内容。\n独立内容。\n独立内容。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position) VALUES
		('ol-1', 'src-1', 1, '第一章', 1, 1, 'structural', 1),
		('ol-2', 'src-1', 1, '第二章', 2, 2, 'structural', 2),
		('ol-3', 'src-1', 1, '第三章', 3, 3, 'structural', 3)`)

	// One shared boundary response listing all three lines: each segment keeps
	// only the line inside its own bounds (buildLLMUnitFromBoundary drops
	// out-of-segment lines), so a single SetResponse works for every worker
	// regardless of which segment it picks up — no sequencing to race on.
	fake.SetResponse("unit_boundary_extract.md", llm.FakeResponse{Output: `{
		"units": [{"center": "章节内容", "content": ["[1] 独立内容。", "[2] 独立内容。", "[3] 独立内容。"]}]
	}`})
	fake.SetResponse("unit_point_extract.md", llm.FakeResponse{Output: `{"center":"章节内容","points":[{"content":"章节知识点","type":"definition"}]}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3 (one per segment, none dropped or duplicated under concurrency=2)", len(units))
	}

	gotLines := map[int]bool{}
	for _, u := range units {
		if u.Status != "completed" {
			t.Errorf("unit %s status = %q, want completed", u.UnitID, u.Status)
		}
		gotLines[u.LineStart] = true
	}
	for _, want := range []int{1, 2, 3} {
		if !gotLines[want] {
			t.Errorf("missing unit anchored at line %d; got units at %v", want, gotLines)
		}
	}
}

func TestTokenOverlap(t *testing.T) {
	if got := tokenOverlap("更新达梦数据库统计信息的方法", "更新达梦数据库统计信息的另一种方法"); got <= 0 {
		t.Errorf("overlap = %v, want > 0 for near-identical sentences sharing most terms", got)
	}
	if got := tokenOverlap("苹果手机的电池续航很长", "今天天气不错适合出门散步"); got > 0.15 {
		t.Errorf("overlap = %v, want a low score for unrelated sentences", got)
	}
	if got := tokenOverlap("", "非空文本"); got != 0 {
		t.Errorf("overlap = %v, want 0 when one side is empty", got)
	}
}

// TestLogDocumentCandidates_CrossSegment verifies the document-level
// diagnostic pass nominates a duplicate restated in a different segment —
// the per-segment adjacent scan's structural blind spot — while staying
// silent about same-segment pairs the adjacency window already judged.
func TestLogDocumentCandidates_CrossSegment(t *testing.T) {
	svc, _, _ := setupPreInsertDedupTestService(t, 0.1, 1)

	segA := Segment{OutlineID: sql.NullString{String: "ol-1", Valid: true}, Title: "费用", LineStart: 1, LineEnd: 20}
	segB := Segment{OutlineID: sql.NullString{String: "ol-2", Valid: true}, Title: "附则", LineStart: 100, LineEnd: 120}

	pool := []unitCandidate{
		{
			id: "cand-a", seg: segA, segIndex: 0, lineStart: 5, lineEnd: 9,
			llm:    llmUnit{UnitID: "1", Center: "差旅住宿费报销限额"},
			points: []llmPoint{{Content: "一线城市住宿费限额每晚 500 元，超出部分不予报销"}},
		},
		{
			id: "cand-b", seg: segB, segIndex: 1, lineStart: 105, lineEnd: 109,
			llm:    llmUnit{UnitID: "1", Center: "住宿费用报销限额规定"},
			points: []llmPoint{{Content: "一线城市住宿费限额每晚 500 元，超出部分自理"}},
		},
		// Same-segment adjacent pair: even if similar, it was dedupCandidates'
		// jurisdiction — must not be re-reported here.
		{
			id: "cand-c", seg: segA, segIndex: 0, lineStart: 10, lineEnd: 12,
			llm:    llmUnit{UnitID: "2", Center: "交通费报销标准"},
			points: []llmPoint{{Content: "高铁二等座标准报销"}},
		},
		{
			id: "cand-d", seg: segA, segIndex: 0, lineStart: 13, lineEnd: 15,
			llm:    llmUnit{UnitID: "3", Center: "交通费报销规定"},
			points: []llmPoint{{Content: "高铁二等座按标准报销，其余自理"}},
		},
	}

	mdLines := make([]string, 120)
	for i := range mdLines {
		mdLines[i] = "占位行"
	}

	pairs := svc.logDocumentCandidates("src-x", mdLines, pool)

	foundCross := false
	for _, p := range pairs {
		if (p.A.UnitID == "cand-a" && p.B.UnitID == "cand-b") || (p.A.UnitID == "cand-b" && p.B.UnitID == "cand-a") {
			foundCross = true
			if !p.CrossSegment() {
				t.Error("cand-a/cand-b should report CrossSegment")
			}
		}
		if (p.A.UnitID == "cand-c" && p.B.UnitID == "cand-d") || (p.A.UnitID == "cand-d" && p.B.UnitID == "cand-c") {
			t.Error("same-segment adjacent pair cand-c/cand-d must not be re-reported by the diagnostic pass")
		}
	}
	if !foundCross {
		t.Error("cross-segment duplicate cand-a/cand-b was not nominated by document-level recall")
	}
}

// TestCollectSegmentCandidates_RetryJoinsCandidatePool pins批次三's bypass
// fix: a unit whose anchors fail to locate is retried through
// unit_extract_retry.md, and the retry result must join the candidate pool —
// getting dedup coverage like any other candidate — instead of being
// inserted directly around it (the old behavior, which stored v5-retry
// twins next to their v6 originals). The split boundary pipeline derives
// bounds programmatically and can't produce a locate failure itself, so the
// scenario is driven through collectSegmentCandidates directly with a
// legacy-shaped output (the shape unit_extract_retry.md still returns).
func TestCollectSegmentCandidates_RetryJoinsCandidatePool(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.1, 1)
	insertSource(t, db, "src-1", "/tmp/unused.md")

	mdLines := []string{"第一部分内容简述。", "第二部分内容详述，其实是同一件事。"}
	seg := Segment{OutlineID: sql.NullString{String: "ol-1", Valid: true}, Title: "第一章", LineStart: 1, LineEnd: 2}

	// Unit 1 locates fine; unit 2's anchors are garbage, so it fails locate
	// and goes down the retry path.
	mainResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "主题概述", LineStart: 1, FirstLineAnchor: "第一部分内容简述。", LineEnd: 1, LastLineAnchor: "第一部分内容简述。"},
			{UnitID: "2", Center: "主题详述", LineStart: 99, FirstLineAnchor: "完全不存在的锚点文本", LineEnd: 99, LastLineAnchor: "完全不存在的锚点文本"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "简述版知识点", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "详述版知识点", Type: "definition"},
		},
	}

	// Retry returns unit 2 correctly anchored on line 2 — adjacent to unit 1,
	// and a duplicate of it per the dedup response below.
	retryResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "主题详述", LineStart: 2, FirstLineAnchor: "第二部分内容详述，其实是同一件事。", LineEnd: 2, LastLineAnchor: "第二部分内容详述，其实是同一件事。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "详述版知识点", Type: "definition"},
		},
	}
	retryB, _ := json.Marshal(retryResp)

	fake.SetResponse("unit_extract_retry.md", llm.FakeResponse{Output: string(retryB)})
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "duplicate", "reason": "同一事实的不同表述"}`})
	fake.SetResponse("unit_dedup_merge.md", llm.FakeResponse{Output: `{"center": "合并后的主题", "points": [{"content": "合并去重后的知识点内容", "type": "rule"}]}`})

	candidates := svc.collectSegmentCandidates(t.Context(), "src-1", seg, 0, mdLines, mainResp, promptVersionSplitExtract)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates (%+v), want 1 — the retry result must be pooled and deduped, not inserted directly", len(candidates), candidates)
	}
	if candidates[0].llm.Center != "合并后的主题" {
		t.Errorf("center = %q, want 合并后的主题 (merged via dedup)", candidates[0].llm.Center)
	}
	if n := countCalls(fake, "unit_extract_retry.md"); n != 1 {
		t.Errorf("unit_extract_retry.md called %d times, want 1", n)
	}
	if n := countCalls(fake, "unit_dedup_classify.md"); n != 1 {
		t.Errorf("unit_dedup_classify.md called %d times, want 1 (the pooled retry candidate is adjacent to unit 1)", n)
	}

	// Nothing may have reached the store on this path — the retry succeeded,
	// so not even an extraction_failed placeholder is written.
	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 0 {
		t.Fatalf("store has %d units, want 0 (candidates are in-memory until insertCandidates)", len(units))
	}
}

// TestExtract_Reextraction_SupersedesPreviousGeneration pins批次五's
// idempotency半: re-running Extract on the same source must replace the
// previous generation (mark it superseded) instead of doubling the stored
// units — the old behavior for a double-triggered unit_extract task.
func TestExtract_Reextraction_SupersedesPreviousGeneration(t *testing.T) {
	svc, fake, db := setupPreInsertDedupTestService(t, 0.1, 1)
	tmpDir := t.TempDir()

	mdPath := writeDedupMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, '第一章', 1, 2, 'structural', 1)`)

	out := mainExtractRespTwoAdjacentUnits()
	fake.SetResponse("unit_boundary_extract.md", llm.FakeResponse{Output: splitBoundaryResp(out)})
	// Extract runs twice below; queue two rounds of per-unit point responses.
	pointResps := splitPointResps(out)
	fake.SetResponseSequence("unit_point_extract.md", append(append([]llm.FakeResponse{}, pointResps...), pointResps...))
	fake.SetResponse("unit_dedup_classify.md", llm.FakeResponse{Output: `{"relation": "parallel", "reason": "不同侧面"}`})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})

	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if err := svc.Extract(t.Context(), "src-1"); err != nil {
		t.Fatalf("second extract: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	var current, superseded int
	for _, u := range units {
		switch u.Lifecycle {
		case LifecycleCurrent:
			current++
		case LifecycleSuperseded:
			superseded++
		}
	}
	if current != 2 {
		t.Errorf("current units = %d, want 2 (only the second generation)", current)
	}
	if superseded != 2 {
		t.Errorf("superseded units = %d, want 2 (the whole first generation)", superseded)
	}
}

// TestExtract_ConcurrentSameSourceRejected pins the source-level mutex: a
// second Extract on a source with one already in flight returns
// ErrExtractionInProgress instead of double-inserting.
func TestExtract_ConcurrentSameSourceRejected(t *testing.T) {
	svc, _, _ := setupPreInsertDedupTestService(t, 0.1, 1)

	if !svc.beginExtract("src-busy") {
		t.Fatal("first beginExtract should succeed")
	}
	defer svc.endExtract("src-busy")

	err := svc.Extract(t.Context(), "src-busy")
	if !errors.Is(err, ErrExtractionInProgress) {
		t.Fatalf("err = %v, want ErrExtractionInProgress", err)
	}
}
