package unit

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

func bleveField(t *testing.T, idx bleve.Index, id, field string) (string, bool) {
	t.Helper()
	req := bleve.NewSearchRequest(bleve.NewDocIDQuery([]string{id}))
	req.Fields = []string{field}
	res, err := idx.Search(req)
	if err != nil {
		t.Fatalf("bleve search: %v", err)
	}
	if len(res.Hits) == 0 {
		return "", false
	}
	v, _ := res.Hits[0].Fields[field].(string)
	return v, true
}

func TestSetUnitLifecycle_CascadesAndReindexes(t *testing.T) {
	svc, _, db := setupTestService(t)
	tmpDir := t.TempDir()

	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	ku := &KnowledgeUnit{SourceID: "src-1", OutlineID: sql.NullString{String: "ol-1", Valid: true},
		Center: "知识管理", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if err := svc.store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}
	kp := &KnowledgePoint{UnitID: ku.UnitID, SourceID: "src-1", Content: "知识管理的定义", PointType: "definition"}
	if err := svc.store.InsertPoint(kp); err != nil {
		t.Fatalf("insert point: %v", err)
	}

	// Seed Bleve as a normal extraction would (lifecycle=current).
	mdBytes, _ := os.ReadFile(mdPath)
	svc.indexUnit(ku, strings.Split(string(mdBytes), "\n"))
	svc.indexPoint(kp)

	var notified []string
	svc.SetWikiNotifier(fakeWikiNotifier{onNotify: func(ids []string) { notified = ids }})

	if err := svc.SetUnitLifecycle([]string{ku.UnitID}, LifecycleSuperseded, "test supersede"); err != nil {
		t.Fatalf("SetUnitLifecycle: %v", err)
	}

	gotKU, err := svc.store.GetUnitByID(ku.UnitID)
	if err != nil {
		t.Fatalf("GetUnitByID: %v", err)
	}
	if gotKU.Lifecycle != LifecycleSuperseded {
		t.Errorf("unit lifecycle = %q, want %q", gotKU.Lifecycle, LifecycleSuperseded)
	}
	if !gotKU.LifecycleChangedAt.Valid {
		t.Error("expected lifecycle_changed_at to be set")
	}

	gotKP, err := svc.store.GetPointByID(kp.PointID)
	if err != nil {
		t.Fatalf("GetPointByID: %v", err)
	}
	if gotKP.Lifecycle != LifecycleSuperseded {
		t.Errorf("point lifecycle = %q, want %q (should cascade from unit)", gotKP.Lifecycle, LifecycleSuperseded)
	}

	if lc, ok := bleveField(t, svc.unitsIndex, ku.UnitID, "lifecycle"); !ok || lc != LifecycleSuperseded {
		t.Errorf("bleve unit lifecycle = %q (ok=%v), want %q", lc, ok, LifecycleSuperseded)
	}
	if lc, ok := bleveField(t, svc.pointsIndex, kp.PointID, "lifecycle"); !ok || lc != LifecycleSuperseded {
		t.Errorf("bleve point lifecycle = %q (ok=%v), want %q", lc, ok, LifecycleSuperseded)
	}

	if len(notified) != 1 || notified[0] != kp.PointID {
		t.Errorf("wiki notifier got %v, want [%s]", notified, kp.PointID)
	}
}

// TestSnapshotAndDeprecate_RestoreLifecycle_PreservesPriorSupersededState
// covers 文件管理 恢复按钮's core correctness requirement: a source's units
// may hold different lifecycle states at delete time (one still current,
// one already superseded by an earlier reupload) — SnapshotAndDeprecate must
// deprecate both, but RestoreLifecycle must only bring the current one back,
// leaving the already-superseded one superseded rather than resurrecting it.
func TestSnapshotAndDeprecate_RestoreLifecycle_PreservesPriorSupersededState(t *testing.T) {
	svc, _, db := setupTestService(t)
	tmpDir := t.TempDir()
	mdPath := writeTestMarkdown(t, tmpDir)
	insertSource(t, db, "src-1", mdPath)
	insertOutlines(t, db, "src-1")

	kuCurrent := &KnowledgeUnit{SourceID: "src-1", Center: "当前知识", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if err := svc.store.InsertUnit(kuCurrent); err != nil {
		t.Fatalf("insert current unit: %v", err)
	}
	kuSuperseded := &KnowledgeUnit{SourceID: "src-1", Center: "旧知识", LineStart: 6, LineEnd: 10, Status: "completed", PromptVersion: "v1"}
	if err := svc.store.InsertUnit(kuSuperseded); err != nil {
		t.Fatalf("insert superseded unit: %v", err)
	}
	if err := svc.SetUnitLifecycle([]string{kuSuperseded.UnitID}, LifecycleSuperseded, "pre-existing reupload"); err != nil {
		t.Fatalf("seed superseded state: %v", err)
	}

	unitIDs := []string{kuCurrent.UnitID, kuSuperseded.UnitID}
	if err := svc.SnapshotAndDeprecate(unitIDs, "source deleted"); err != nil {
		t.Fatalf("SnapshotAndDeprecate: %v", err)
	}

	gotCurrent, _ := svc.store.GetUnitByID(kuCurrent.UnitID)
	gotSuperseded, _ := svc.store.GetUnitByID(kuSuperseded.UnitID)
	if gotCurrent.Lifecycle != LifecycleDeprecated || gotSuperseded.Lifecycle != LifecycleDeprecated {
		t.Fatalf("expected both deprecated after SnapshotAndDeprecate, got current=%q superseded=%q",
			gotCurrent.Lifecycle, gotSuperseded.Lifecycle)
	}

	if err := svc.RestoreLifecycle(unitIDs, "source restored"); err != nil {
		t.Fatalf("RestoreLifecycle: %v", err)
	}

	gotCurrent, err := svc.store.GetUnitByID(kuCurrent.UnitID)
	if err != nil {
		t.Fatalf("GetUnitByID current: %v", err)
	}
	if gotCurrent.Lifecycle != LifecycleCurrent {
		t.Errorf("current unit's lifecycle after restore = %q, want current", gotCurrent.Lifecycle)
	}

	gotSuperseded, err = svc.store.GetUnitByID(kuSuperseded.UnitID)
	if err != nil {
		t.Fatalf("GetUnitByID superseded: %v", err)
	}
	if gotSuperseded.Lifecycle != LifecycleSuperseded {
		t.Errorf("previously-superseded unit's lifecycle after restore = %q, want superseded (must not be resurrected to current)", gotSuperseded.Lifecycle)
	}

	// Snapshot column must be cleared so a later delete/restore cycle doesn't
	// read a stale value.
	groups, err := svc.store.GroupUnitIDsByLifecycleBeforeDelete(unitIDs)
	if err != nil {
		t.Fatalf("GroupUnitIDsByLifecycleBeforeDelete: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("expected snapshot cleared after restore, got groups=%+v", groups)
	}
}

func TestRestoreLifecycle_EmptyIDsNoop(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.RestoreLifecycle(nil, "test"); err != nil {
		t.Fatalf("expected no-op, got error: %v", err)
	}
}

func TestSetUnitLifecycle_RejectsInvalidState(t *testing.T) {
	svc, _, _ := setupTestService(t)
	err := svc.SetUnitLifecycle([]string{"u1"}, "bogus", "test")
	if err == nil {
		t.Fatal("expected error for invalid lifecycle state")
	}
}

func TestSetUnitLifecycle_EmptyIDsNoop(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.SetUnitLifecycle(nil, LifecycleCurrent, "test"); err != nil {
		t.Fatalf("expected no-op, got error: %v", err)
	}
}

type fakeWikiNotifier struct {
	onNotify func(pointIDs []string)
}

func (f fakeWikiNotifier) NotifyPointsLifecycleChanged(pointIDs []string) error {
	if f.onNotify != nil {
		f.onNotify(pointIDs)
	}
	return nil
}
