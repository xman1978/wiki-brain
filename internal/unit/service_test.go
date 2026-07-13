package unit

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
)

func setupTestService(t *testing.T) (*Service, *llm.FakeClient, *sql.DB) {
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
			SegmentMaxChars: 4000,
			MinSegmentChars: 400,
		},
	}

	svc := NewService(unitStore, sourceStore, &semanticAwareFakeClient{FakeClient: fake}, idxMgr.Units, idxMgr.Points, q, cfg)
	return svc, fake, db
}

func insertSource(t *testing.T, db *sql.DB, sourceID, mdPath string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES (?, 'Test', 'markdown', 'test.md', '/tmp/test.md', ?, 'completed')`, sourceID, mdPath)
	if err != nil {
		t.Fatalf("insert source: %v", err)
	}
}

func insertOutlines(t *testing.T, db *sql.DB, sourceID string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', ?, 1, '第一章', 1, 10, 'structural', 1)`, sourceID)
	if err != nil {
		t.Fatalf("insert outline: %v", err)
	}
}

func writeTestMarkdown(t *testing.T, dir string) string {
	t.Helper()
	mdPath := filepath.Join(dir, "test.md")
	content := "# 第一章\n\n知识管理是组织和个人对知识进行系统管理的过程。\n它包括知识的获取、存储、共享和应用。\n知识管理的核心目标是提高组织的创新能力和竞争力。\n有效的知识管理需要技术平台和文化支持。\n知识分为显性知识和隐性知识。\n显性知识可以文档化，隐性知识需要通过实践传递。\n知识管理系统是支持知识管理过程的信息系统。\n它通常包括文档管理、搜索引擎和协作工具。"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	return mdPath
}

func TestExtract_HappyPath(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	extractResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "知识管理概述", LineStart: 1, FirstLineAnchor: "# 第一章", LineEnd: 6, LastLineAnchor: "有效的知识管理需要技术平台和文化支持。"},
			{UnitID: "2", Center: "知识管理系统", LineStart: 7, FirstLineAnchor: "知识分为显性知识和隐性知识。", LineEnd: 10, LastLineAnchor: "它通常包括文档管理、搜索引擎和协作工具。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "知识管理是组织和个人对知识进行系统管理的过程", Type: "definition"},
			{PointID: "2", UnitID: "1", Content: "知识分为显性知识和隐性知识", Type: "definition"},
			{PointID: "3", UnitID: "2", Content: "知识管理系统包括文档管理、搜索引擎和协作工具", Type: "method"},
		},
	}
	setSplitExtractFakes(t, fake, extractResp)

	kpnResp := kpnOutput{Relations: []kpnRelation{}}
	kpnJSON, _ := json.Marshal(kpnResp)
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: string(kpnJSON)})

	err := svc.Extract(t.Context(), "src-1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	units, err := svc.store.GetUnitsBySourceID("src-1")
	if err != nil {
		t.Fatalf("get units: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}

	if units[0].Center != "知识管理概述" {
		t.Errorf("unit[0].center = %q, want 知识管理概述", units[0].Center)
	}
	if units[0].Status != "completed" {
		t.Errorf("unit[0].status = %q, want completed", units[0].Status)
	}
	if units[0].LineStart != 1 || units[0].LineEnd != 6 {
		t.Errorf("unit[0] range = %d-%d, want 1-6", units[0].LineStart, units[0].LineEnd)
	}

	points, _ := svc.store.GetPointsByUnitID(units[0].UnitID)
	if len(points) != 2 {
		t.Errorf("unit[0] has %d points, want 2", len(points))
	}

	points2, _ := svc.store.GetPointsByUnitID(units[1].UnitID)
	if len(points2) != 1 {
		t.Errorf("unit[1] has %d points, want 1", len(points2))
	}
}

// TestCollectSegmentCandidates_WidensUnitBoundsFromPointAnchors is the
// regression test for the real anchor-width bug (docs/impl/mvp/unit.md 2.4):
// a unit declares a narrow line range (just its table's header row here)
// while its own knowledge point draws from a row further down, and
// WidenBoundsFromPoints must widen the candidate to the union. The split
// boundary pipeline derives unit bounds programmatically, so per-point
// anchors only arrive via the legacy-shaped outputs that
// unit_extract_retry.md and unit_gap_extract.md still produce —
// collectSegmentCandidates is the shared entry point they all funnel
// through, so it is exercised directly here.
func TestCollectSegmentCandidates_WidensUnitBoundsFromPointAnchors(t *testing.T) {
	svc, _, db := setupTestService(t)
	insertSource(t, db, "src-1", "/tmp/unused.md")

	mdLines := []string{"# 标题", "参数A建议值说明", "参数B建议值说明"}
	seg := Segment{LineStart: 1, LineEnd: 3}

	output := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "参数配置说明", LineStart: 1, FirstLineAnchor: "# 标题", LineEnd: 1, LastLineAnchor: "# 标题"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "参数A的建议值说明", Type: "rule", LineStart: 2, FirstLineAnchor: "参数A建议值说明", LineEnd: 2, LastLineAnchor: "参数A建议值说明"},
			{PointID: "2", UnitID: "1", Content: "参数B的建议值说明", Type: "rule", LineStart: 3, FirstLineAnchor: "参数B建议值说明", LineEnd: 3, LastLineAnchor: "参数B建议值说明"},
		},
	}

	candidates := svc.collectSegmentCandidates(t.Context(), "src-1", seg, 0, mdLines, output, promptVersionExtractRetry)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if candidates[0].lineStart != 1 || candidates[0].lineEnd != 3 {
		t.Errorf("candidate range = %d-%d, want 1-3 (widened by its points' own anchors, not left at the unit-level 1-1)", candidates[0].lineStart, candidates[0].lineEnd)
	}
}

// TestCollectSegmentCandidates_HallucinatedAnchorDoesNotPublishFailureMarker
// is the regression test for the a100c7a5 incident's structural guarantee:
// an anchor that doesn't exist anywhere in the segment can never silently
// become a line range. It must fail validation and, after the retry prompt
// also can't produce a locatable anchor, remain an in-memory failure rather
// than escaping the later atomic publication boundary. The
// legacy-shaped anchors now only enter through unit_extract_retry.md's
// output, whose locate/validate handling lives in collectSegmentCandidates.
func TestCollectSegmentCandidates_HallucinatedAnchorDoesNotPublishFailureMarker(t *testing.T) {
	svc, fake, db := setupTestService(t)
	insertSource(t, db, "src-1", "/tmp/unused.md")

	mdLines := []string{
		"# 配置 SSH 无密码登录通道",
		"生成 RSA 密钥对并交换公钥",
		"# 启动停止 RAC 集群",
		"停止数据库需执行 srvctl stop database",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	output := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "配置 SSH 无密码登录通道",
				FirstLineAnchor: "# 配置 SSH 无密码登录通道",
				LastLineAnchor:  "这段文字在原文中根本不存在"},
		},
		Points: []llmPoint{{PointID: "1", UnitID: "1", Content: "生成密钥并交换公钥", Type: "method"}},
	}
	extractJSON, _ := json.Marshal(output)
	// Retry attempt also can't locate a real anchor — same hallucination.
	fake.SetResponse("unit_extract_retry.md", llm.FakeResponse{Output: string(extractJSON)})

	candidates := svc.collectSegmentCandidates(t.Context(), "src-1", seg, 0, mdLines, output, promptVersionExtractRetry)
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0 (hallucinated anchor must never become a pooled candidate)", len(candidates))
	}

	units, err := svc.store.GetUnitsBySourceID("src-1")
	if err != nil {
		t.Fatalf("get units: %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("got %d stored units, want 0 before atomic publication", len(units))
	}
}

// TestLocateUnitBounds_KnownLimitation_WrongButExistingAnchor documents,
// rather than hides, the boundary of what this mechanism fixes: it only
// guarantees a resolved unit is built from real, in-segment text — it cannot
// tell a real anchor that belongs to the wrong section from a correct one.
// If the model picks a last_line_anchor that legitimately exists but at the
// start of the next, unrelated section, LocateUnitBounds will resolve it
// there (structurally impossible to over-run the segment, semantically
// still capable of choosing wrong within it).
func TestLocateUnitBounds_KnownLimitation_WrongButExistingAnchor(t *testing.T) {
	mdLines := []string{
		"# 配置 SSH 无密码登录通道",
		"生成 RSA 密钥对并交换公钥",
		"# 启动停止 RAC 集群",
		"停止数据库需执行 srvctl stop database",
	}
	seg := Segment{LineStart: 1, LineEnd: 4}

	lineStart, lineEnd, _, ok := LocateUnitBounds(mdLines, seg,
		unverified, "# 配置 SSH 无密码登录通道", unverified, "# 启动停止 RAC 集群", seg.LineStart)
	if !ok {
		t.Fatal("expected the (wrong-scope but real) anchor to resolve")
	}
	if lineStart != 1 || lineEnd != 3 {
		t.Fatalf("got %d-%d — matches the known limitation description (wrong section, but a real line)", lineStart, lineEnd)
	}
}

func TestExtract_AllFailed(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	fake.SetResponse("unit_boundary_extract.md", llm.FakeResponse{Err: llm.ErrTimeout})

	err := svc.Extract(t.Context(), "src-1")
	if err == nil {
		t.Fatal("extract should return an error when every segment fails")
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 0 {
		t.Errorf("got %d units, want 0 (all failed)", len(units))
	}
}

func TestExtract_KPNGeneration(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	extractResp := extractOutput{
		Units: []llmUnit{
			{UnitID: "1", Center: "单元A", LineStart: 1, FirstLineAnchor: "# 第一章", LineEnd: 5, LastLineAnchor: "知识管理的核心目标是提高组织的创新能力和竞争力。"},
			{UnitID: "2", Center: "单元B", LineStart: 6, FirstLineAnchor: "有效的知识管理需要技术平台和文化支持。", LineEnd: 10, LastLineAnchor: "它通常包括文档管理、搜索引擎和协作工具。"},
		},
		Points: []llmPoint{
			{PointID: "1", UnitID: "1", Content: "点A", Type: "definition"},
			{PointID: "2", UnitID: "2", Content: "点B", Type: "rule"},
		},
	}
	setSplitExtractFakes(t, fake, extractResp)

	// KPN response uses actual point_ids - but we need to use a callback pattern
	// Since the fake client doesn't support dynamic responses, we'll set up a response
	// that returns relations using placeholder IDs that won't match
	// In a real scenario the LLM sees actual UUIDs in input
	// For testing, we verify the KPN call was made
	kpnResp := kpnOutput{Relations: []kpnRelation{}}
	kpnJSON, _ := json.Marshal(kpnResp)
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: string(kpnJSON)})

	err := svc.Extract(t.Context(), "src-1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	calls := fake.Calls()
	kpnCalled := false
	for _, c := range calls {
		if c.PromptFile == "kpn_extract.md" {
			kpnCalled = true
		}
	}
	if !kpnCalled {
		t.Error("KPN extraction was not called")
	}
}

func TestExtract_ConceptMatch(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('d-1', 'TestDomain')`)
	db.Exec(`INSERT INTO concepts (concept_id, domain_id, name, description) VALUES ('c-1', 'd-1', '知识管理', '知识管理领域')`)

	extractResp := extractOutput{
		Units:  []llmUnit{{UnitID: "1", Center: "知识管理概述", LineStart: 1, FirstLineAnchor: "# 第一章", LineEnd: 10, LastLineAnchor: "知识管理的核心目标是提高组织的创新能力和竞争力。"}},
		Points: []llmPoint{{PointID: "1", UnitID: "1", Content: "知识管理定义", Type: "definition"}},
	}
	setSplitExtractFakes(t, fake, extractResp)

	kpnResp := kpnOutput{Relations: []kpnRelation{}}
	kpnJSON, _ := json.Marshal(kpnResp)
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: string(kpnJSON)})

	// We can't predict the unit_id UUID, so we'll use a dynamic approach
	// The concept match response will be set after extraction to use the real unit_id
	// For this test, we just verify the call was made
	conceptResp := conceptMatchOutput{Matches: []conceptMatch{}}
	conceptJSON, _ := json.Marshal(conceptResp)
	fake.SetResponse("unit_concept_match.md", llm.FakeResponse{Output: string(conceptJSON)})

	err := svc.Extract(t.Context(), "src-1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	calls := fake.Calls()
	conceptCalled := false
	for _, c := range calls {
		if c.PromptFile == "unit_concept_match.md" {
			conceptCalled = true
		}
	}
	if !conceptCalled {
		t.Error("concept match was not called")
	}
}

func TestExtract_KPNFailureDoesNotBlock(t *testing.T) {
	svc, fake, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	extractResp := extractOutput{
		Units:  []llmUnit{{UnitID: "1", Center: "主题", LineStart: 1, FirstLineAnchor: "# 第一章", LineEnd: 10, LastLineAnchor: "知识管理的核心目标是提高组织的创新能力和竞争力。"}},
		Points: []llmPoint{{PointID: "1", UnitID: "1", Content: "内容", Type: "definition"}},
	}
	setSplitExtractFakes(t, fake, extractResp)

	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Err: llm.ErrTimeout})

	err := svc.Extract(t.Context(), "src-1")
	if err != nil {
		t.Fatalf("extract should not fail when KPN fails: %v", err)
	}

	units, _ := svc.store.GetUnitsBySourceID("src-1")
	if len(units) != 1 {
		t.Errorf("got %d units, want 1", len(units))
	}
	if units[0].Status != "completed" {
		t.Errorf("status = %q, want completed", units[0].Status)
	}
}

func TestTriggerExtract_RequiresCompletedSource(t *testing.T) {
	svc, _, db := setupTestService(t)

	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('src-pending', 'Test', 'markdown', 'test.md', '/tmp/test.md', '/tmp/test.md', 'pending')`)
	if err != nil {
		t.Fatal(err)
	}

	err = svc.TriggerExtract("src-pending")
	if err == nil {
		t.Fatal("expected error for non-completed source")
	}
}
