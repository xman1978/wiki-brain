package unit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
)

// setupReuploadTest wires source.Service and unit.Service together the same
// way cmd/server/main.go does (minus the async queue — tests drive
// Import/Process/Extract/CompleteShadowSwap synchronously), so the full
// Shadow Source reupload lifecycle (docs/impl/v1/lifecycle.md 步骤 2) can be
// exercised end to end with a fake LLM client.
func setupReuploadTest(t *testing.T) (*source.Service, *Service, *llm.FakeClient, string, *index.Manager, *queue.Queue) {
	t.Helper()
	db := foundation.NewTestDB(t)
	tmpDir := t.TempDir()
	for _, dir := range []string{"data/sources/original", "data/sources/html", "data/sources/markdown"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	sourceStore := source.NewStore(db)
	unitStore := NewStore(db)
	fake := llm.NewFakeClient()
	q := queue.New(100)

	idxMgr, err := index.NewManager(filepath.Join(tmpDir, "index"))
	if err != nil {
		t.Fatalf("index.NewManager: %v", err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	cfg := &config.Config{
		Source: config.SourceConfig{SegmentMaxChars: 4000, MinSegmentChars: 10},
	}
	models := llm.StaticPurposeModels{
		"default":    {Model: "test", MaxInputTokens: 100000, MaxOutputTokens: 4096},
		"extraction": {Model: "test", MaxInputTokens: 100000, MaxOutputTokens: 4096},
	}

	sourceSvc := source.NewService(sourceStore, nil, fake, models, idxMgr.Outlines, q, cfg, tmpDir)
	sourceSvc.SetUnitIndexes(idxMgr.Units, idxMgr.Points)

	unitSvc := NewService(unitStore, sourceStore, &semanticAwareFakeClient{FakeClient: fake}, idxMgr.Units, idxMgr.Points, q, cfg)
	sourceSvc.SetLifecycleSetter(unitSvc)

	return sourceSvc, unitSvc, fake, tmpDir, idxMgr, q
}

// setExtractResponse configures the fake unit_boundary_extract.md /
// unit_point_extract.md / kpn_extract.md responses for the next call to
// unitSvc.Extract: one unit spanning lineStart..lineEnd with a single
// knowledge point.
func setExtractResponse(t *testing.T, fake *llm.FakeClient, center, pointContent string, lineStart, lineEnd int) {
	t.Helper()
	setSplitExtractFakes(t, fake, extractOutput{
		Units:  []llmUnit{{UnitID: "1", Center: center, LineStart: lineStart, LineEnd: lineEnd}},
		Points: []llmPoint{{PointID: "1", UnitID: "1", Content: pointContent, Type: "definition"}},
	})
	fake.SetResponse("kpn_extract.md", llm.FakeResponse{Output: `{"relations":[]}`})
}

// seedMarkdown copies the just-imported original file to its markdown path
// so Process's convertToMarkdown sees it as already converted and skips the
// (nil, in these tests) FileView client. Real Import() flows rely on a real
// FileViewClient for this step; that's out of scope for the lifecycle module.
func seedMarkdown(t *testing.T, tmpDir string, src *source.Source) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmpDir, src.OriginalPath))
	if err != nil {
		t.Fatalf("read original for markdown seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, src.MarkdownPath), data, 0644); err != nil {
		t.Fatalf("seed markdown: %v", err)
	}
}

func importProcessExtract(t *testing.T, sourceSvc *source.Service, unitSvc *Service, sourceID string) {
	t.Helper()
	if err := sourceSvc.Process(context.Background(), sourceID); err != nil {
		t.Fatalf("Process(%s): %v", sourceID, err)
	}
	if err := unitSvc.Extract(context.Background(), sourceID); err != nil {
		t.Fatalf("Extract(%s): %v", sourceID, err)
	}
}

func awaitQueuedSourceID(t *testing.T, tasks <-chan string, want string) {
	t.Helper()
	select {
	case got := <-tasks:
		if got != want {
			t.Fatalf("queued source_id = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for queued source_id %q", want)
	}
}

func TestReuploadLifecycle_HappyPathSwap(t *testing.T) {
	sourceSvc, unitSvc, fake, tmpDir, idxMgr, _ := setupReuploadTest(t)
	// unit.Service.Extract reads markdown via a bare os.ReadFile(src.MarkdownPath)
	// with no baseDir join (see cmd/server/main.go: baseDir = os.Getwd() at
	// startup, so relative paths resolve against CWD in production). Match
	// that precondition here instead of the source-package convention of
	// absolute paths, since this test drives both packages against the same rows.
	t.Chdir(tmpDir)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "旧版摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := sourceSvc.Import(context.Background(), "policy.md", strings.NewReader("# 政策\n\n旧版内容第一行。\n旧版内容第二行。"))
	if err != nil {
		t.Fatalf("Import target: %v", err)
	}
	seedMarkdown(t, tmpDir, target)
	setExtractResponse(t, fake, "旧版政策", "旧版规定内容", 1, 3)
	importProcessExtract(t, sourceSvc, unitSvc, target.SourceID)

	oldUnits, err := unitSvc.store.GetUnitsBySourceID(target.SourceID)
	if err != nil {
		t.Fatalf("get old units: %v", err)
	}
	if len(oldUnits) != 1 {
		t.Fatalf("got %d old units, want 1", len(oldUnits))
	}
	oldUnitID := oldUnits[0].UnitID
	if oldUnits[0].Lifecycle != LifecycleCurrent {
		t.Fatalf("old unit lifecycle = %q before reupload, want current", oldUnits[0].Lifecycle)
	}

	// Reupload: new content processed entirely in a hidden shadow.
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "新版摘要"})
	shadow, err := sourceSvc.ImportShadow(context.Background(), target.SourceID, "policy-v2.md", strings.NewReader("# 政策\n\n新版内容第一行。\n新版内容第二行。"))
	if err != nil {
		t.Fatalf("ImportShadow: %v", err)
	}
	seedMarkdown(t, tmpDir, shadow)

	// Target must stay untouched and current while the shadow is mid-flight.
	stillTarget, err := sourceSvc.Store().GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("GetByID target mid-flight: %v", err)
	}
	if stillTarget.Status != "completed" {
		t.Errorf("target status mid-flight = %q, want completed (untouched)", stillTarget.Status)
	}
	midUnit, _ := unitSvc.store.GetUnitByID(oldUnitID)
	if midUnit.Lifecycle != LifecycleCurrent {
		t.Errorf("old unit lifecycle mid-flight = %q, want current (untouched until swap)", midUnit.Lifecycle)
	}

	setExtractResponse(t, fake, "新版政策", "新版规定内容", 1, 3)
	importProcessExtract(t, sourceSvc, unitSvc, shadow.SourceID)

	if err := sourceSvc.CompleteShadowSwap(context.Background(), shadow.SourceID); err != nil {
		t.Fatalf("CompleteShadowSwap: %v", err)
	}

	// Old KU is superseded, keeps its own source_id (target's).
	oldAfter, err := unitSvc.store.GetUnitByID(oldUnitID)
	if err != nil {
		t.Fatalf("get old unit after swap: %v", err)
	}
	if oldAfter.Lifecycle != LifecycleSuperseded {
		t.Errorf("old unit lifecycle after swap = %q, want superseded", oldAfter.Lifecycle)
	}
	if !oldAfter.LifecycleChangedAt.Valid {
		t.Error("old unit lifecycle_changed_at should be set")
	}

	// New KU (from shadow) now belongs to target and is current.
	allUnits, err := unitSvc.store.GetUnitsBySourceID(target.SourceID)
	if err != nil {
		t.Fatalf("get units after swap: %v", err)
	}
	if len(allUnits) != 2 {
		t.Fatalf("got %d units under target after swap, want 2 (old superseded + new current)", len(allUnits))
	}
	var newUnit *KnowledgeUnit
	for i := range allUnits {
		if allUnits[i].UnitID != oldUnitID {
			newUnit = &allUnits[i]
		}
	}
	if newUnit == nil {
		t.Fatal("expected a new unit distinct from the old one")
	}
	if newUnit.Lifecycle != LifecycleCurrent {
		t.Errorf("new unit lifecycle = %q, want current", newUnit.Lifecycle)
	}
	if newUnit.Center != "新版政策" {
		t.Errorf("new unit center = %q, want 新版政策", newUnit.Center)
	}

	oldContent, err := unitSvc.readUnitContent(oldAfter)
	if err != nil {
		t.Fatalf("readUnitContent old: %v", err)
	}
	if !strings.Contains(oldContent, "旧版内容第一行") {
		t.Errorf("superseded unit content = %q, want archived old markdown slice", oldContent)
	}
	if strings.Contains(oldContent, "新版内容第一行") {
		t.Errorf("superseded unit content must not be sliced from the new markdown: %q", oldContent)
	}
	newContent, err := unitSvc.readUnitContent(newUnit)
	if err != nil {
		t.Fatalf("readUnitContent new: %v", err)
	}
	if !strings.Contains(newContent, "新版内容第一行") {
		t.Errorf("current unit content = %q, want new markdown slice", newContent)
	}

	// Shadow row is gone.
	if _, err := sourceSvc.Store().GetByID(shadow.SourceID); err == nil {
		t.Error("shadow row should be deleted after swap")
	}

	// The shadow's pipeline indexed its units/points/outlines under the shadow
	// source_id; after the swap the Bleve documents must carry the target's id,
	// or Retrieval's source filters silently drop every hit from this source.
	if sid, ok := bleveField(t, idxMgr.Units, newUnit.UnitID, "source_id"); !ok || sid != target.SourceID {
		t.Errorf("bleve unit source_id after swap = %q (found=%v), want %q", sid, ok, target.SourceID)
	}
	newPoints, err := unitSvc.store.GetPointsByUnitID(newUnit.UnitID)
	if err != nil || len(newPoints) == 0 {
		t.Fatalf("get new unit's points: %v (n=%d)", err, len(newPoints))
	}
	if sid, ok := bleveField(t, idxMgr.Points, newPoints[0].PointID, "source_id"); !ok || sid != target.SourceID {
		t.Errorf("bleve point source_id after swap = %q (found=%v), want %q", sid, ok, target.SourceID)
	}
	outlinesAfter, err := sourceSvc.Store().GetOutlines(target.SourceID)
	if err != nil || len(outlinesAfter) == 0 {
		t.Fatalf("get target outlines after swap: %v (n=%d)", err, len(outlinesAfter))
	}
	if sid, ok := bleveField(t, idxMgr.Outlines, outlinesAfter[0].OutlineID, "source_id"); !ok || sid != target.SourceID {
		t.Errorf("bleve outline source_id after swap = %q (found=%v), want %q", sid, ok, target.SourceID)
	}

	// Target metadata reflects the shadow's own processing (except title).
	finalTarget, err := sourceSvc.Store().GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("GetByID target after swap: %v", err)
	}
	if finalTarget.FileName != "policy-v2.md" {
		t.Errorf("file_name after swap = %q, want policy-v2.md", finalTarget.FileName)
	}
	if !finalTarget.Summary.Valid || finalTarget.Summary.String != "新版摘要" {
		t.Errorf("summary after swap = %v, want 新版摘要 (copied from shadow)", finalTarget.Summary)
	}

	// Markdown file content now reflects the new (shadow) content, and the
	// old content was archived for traceability.
	md, err := sourceSvc.GetMarkdown(target.SourceID)
	if err != nil {
		t.Fatalf("GetMarkdown: %v", err)
	}
	if !strings.Contains(md, "新版内容") {
		t.Errorf("target markdown after swap does not contain new content: %s", md)
	}

	archivedRoot := filepath.Join(tmpDir, "data", "sources", "archived", target.SourceID)
	entries, err := os.ReadDir(archivedRoot)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected an archived snapshot dir under %s, err=%v", archivedRoot, err)
	}
	archivedFiles, err := os.ReadDir(filepath.Join(archivedRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("read archived snapshot: %v", err)
	}
	foundOldContent := false
	for _, f := range archivedFiles {
		data, _ := os.ReadFile(filepath.Join(archivedRoot, entries[0].Name(), f.Name()))
		if strings.Contains(string(data), "旧版内容") {
			foundOldContent = true
		}
	}
	if !foundOldContent {
		t.Error("archived snapshot should retain the old content for traceability")
	}

	if finalTarget.Version != 2 {
		t.Errorf("version after one reupload = %d, want 2", finalTarget.Version)
	}
	versions, err := sourceSvc.Store().GetSourceVersions(target.SourceID)
	if err != nil {
		t.Fatalf("GetSourceVersions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versions = %+v, want exactly one entry for version 1", versions)
	}
}

func TestReuploadLifecycle_ShadowProcessFailureLeavesTargetUntouched(t *testing.T) {
	sourceSvc, unitSvc, fake, tmpDir, _, _ := setupReuploadTest(t)
	t.Chdir(tmpDir)
	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := sourceSvc.Import(context.Background(), "a.md", strings.NewReader("# A\n\n内容一。\n内容二。"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	seedMarkdown(t, tmpDir, target)
	setExtractResponse(t, fake, "A中心", "A的知识点", 1, 3)
	importProcessExtract(t, sourceSvc, unitSvc, target.SourceID)

	shadow, err := sourceSvc.ImportShadow(context.Background(), target.SourceID, "a-v2.md", strings.NewReader("# A\n\n新内容"))
	if err != nil {
		t.Fatalf("ImportShadow: %v", err)
	}
	seedMarkdown(t, tmpDir, shadow)

	// Simulate a source_process-level failure on the shadow (e.g. FileView
	// error) without touching the target at all.
	errMsg := "simulated conversion failure"
	if err := sourceSvc.Store().UpdateStatus(shadow.SourceID, "failed", &errMsg); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	targetAfter, err := sourceSvc.Store().GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("GetByID target: %v", err)
	}
	if targetAfter.Status != "completed" {
		t.Errorf("target status = %q, want completed (unaffected)", targetAfter.Status)
	}
	units, _ := unitSvc.store.GetUnitsBySourceID(target.SourceID)
	if len(units) != 1 || units[0].Lifecycle != LifecycleCurrent {
		t.Errorf("target units should remain untouched and current: %+v", units)
	}

	// Retry resumes the failed shadow rather than starting a new one.
	retried, err := sourceSvc.ReuploadRetry(context.Background(), target.SourceID)
	if err != nil {
		t.Fatalf("ReuploadRetry: %v", err)
	}
	if retried.SourceID != shadow.SourceID {
		t.Errorf("retry should resume the same shadow, got %s want %s", retried.SourceID, shadow.SourceID)
	}

	// Simulate the queue re-running the shadow's pipeline after retry.
	setExtractResponse(t, fake, "A中心v2", "A的知识点v2", 1, 3)
	importProcessExtract(t, sourceSvc, unitSvc, shadow.SourceID)
	if err := sourceSvc.CompleteShadowSwap(context.Background(), shadow.SourceID); err != nil {
		t.Fatalf("CompleteShadowSwap after retry: %v", err)
	}

	finalTarget, err := sourceSvc.Store().GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("GetByID target after retry+swap: %v", err)
	}
	if finalTarget.FileName != "a-v2.md" {
		t.Errorf("file_name after retry+swap = %q, want a-v2.md", finalTarget.FileName)
	}
}

func TestReuploadLifecycle_SemanticFailureRetriesUnitStageThenSwaps(t *testing.T) {
	sourceSvc, unitSvc, fake, tmpDir, _, q := setupReuploadTest(t)
	t.Chdir(tmpDir)

	sourceTasks := make(chan string, 4)
	unitTasks := make(chan string, 4)
	q.RegisterHandler(queue.TaskTypeSourceProcess, func(payload interface{}) {
		sourceTasks <- payload.(queue.SourceTask).SourceID
	})
	q.RegisterHandler(queue.TaskTypeUnitExtract, func(payload interface{}) {
		unitTasks <- payload.(queue.UnitTask).SourceID
	})
	q.Start()
	t.Cleanup(q.Shutdown)

	fake.SetResponse("source_summary.md", llm.FakeResponse{Output: "摘要"})
	fake.SetResponse("source_domain_match.md", llm.FakeResponse{Output: `{"domain_id": null}`})

	target, err := sourceSvc.Import(context.Background(), "policy.md", strings.NewReader("# Policy\n\nOld content."))
	if err != nil {
		t.Fatalf("Import target: %v", err)
	}
	awaitQueuedSourceID(t, sourceTasks, target.SourceID)
	seedMarkdown(t, tmpDir, target)
	setExtractResponse(t, fake, "Old policy", "Old fact", 1, 3)
	importProcessExtract(t, sourceSvc, unitSvc, target.SourceID)
	awaitQueuedSourceID(t, unitTasks, target.SourceID)

	shadow, err := sourceSvc.ImportShadow(context.Background(), target.SourceID, "policy-v2.md", strings.NewReader("# Policy\n\nNew content."))
	if err != nil {
		t.Fatalf("ImportShadow: %v", err)
	}
	awaitQueuedSourceID(t, sourceTasks, shadow.SourceID)
	seedMarkdown(t, tmpDir, shadow)
	if err := sourceSvc.Process(context.Background(), shadow.SourceID); err != nil {
		t.Fatalf("Process shadow: %v", err)
	}
	awaitQueuedSourceID(t, unitTasks, shadow.SourceID)

	setExtractResponse(t, fake, "New policy", "New fact", 1, 3)
	fake.SetResponse("unit_semantics_extract.md", llm.FakeResponse{Err: errors.New("semantic extraction failed")})
	if err := sourceSvc.Store().UpdateUnitsStatus(shadow.SourceID, "processing"); err != nil {
		t.Fatalf("mark shadow units processing: %v", err)
	}
	if err := unitSvc.Extract(context.Background(), shadow.SourceID); err == nil || !strings.Contains(err.Error(), "semantic extraction failed") {
		t.Fatalf("shadow Extract err = %v, want semantic extraction failure", err)
	}
	if err := sourceSvc.Store().UpdateUnitsStatus(shadow.SourceID, "failed"); err != nil {
		t.Fatalf("mark shadow units failed: %v", err)
	}

	failedShadow, err := sourceSvc.Store().GetByID(shadow.SourceID)
	if err != nil {
		t.Fatalf("get failed shadow: %v", err)
	}
	if failedShadow.Status != "completed" || failedShadow.UnitsStatus != "failed" {
		t.Fatalf("failed shadow status=%q units_status=%q, want completed/failed", failedShadow.Status, failedShadow.UnitsStatus)
	}

	retried, err := sourceSvc.ReuploadRetry(context.Background(), target.SourceID)
	if err != nil {
		t.Fatalf("ReuploadRetry after semantic failure: %v", err)
	}
	if retried.SourceID != shadow.SourceID {
		t.Fatalf("retried shadow = %q, want %q", retried.SourceID, shadow.SourceID)
	}
	awaitQueuedSourceID(t, unitTasks, shadow.SourceID)
	select {
	case got := <-sourceTasks:
		t.Fatalf("unit-stage retry unnecessarily enqueued source_process for %q", got)
	default:
	}
	pendingShadow, err := sourceSvc.Store().GetByID(shadow.SourceID)
	if err != nil {
		t.Fatalf("get pending shadow: %v", err)
	}
	if pendingShadow.Status != "completed" || pendingShadow.UnitsStatus != "pending" {
		t.Fatalf("retry status=%q units_status=%q, want completed/pending", pendingShadow.Status, pendingShadow.UnitsStatus)
	}

	setExtractResponse(t, fake, "New policy", "New fact", 1, 3)
	fake.SetResponse("unit_semantics_extract.md", llm.FakeResponse{Err: errors.New("no response configured")})
	if err := sourceSvc.Store().UpdateUnitsStatus(shadow.SourceID, "processing"); err != nil {
		t.Fatalf("mark retried units processing: %v", err)
	}
	if err := unitSvc.Extract(context.Background(), shadow.SourceID); err != nil {
		t.Fatalf("retried Extract: %v", err)
	}
	if err := sourceSvc.Store().UpdateUnitsStatus(shadow.SourceID, "completed"); err != nil {
		t.Fatalf("mark retried units completed: %v", err)
	}
	if err := sourceSvc.CompleteShadowSwap(context.Background(), shadow.SourceID); err != nil {
		t.Fatalf("CompleteShadowSwap after unit retry: %v", err)
	}

	finalTarget, err := sourceSvc.Store().GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("get target after swap: %v", err)
	}
	if finalTarget.FileName != "policy-v2.md" || finalTarget.UnitsStatus != "completed" {
		t.Fatalf("target after swap file=%q units_status=%q, want policy-v2.md/completed", finalTarget.FileName, finalTarget.UnitsStatus)
	}
}
