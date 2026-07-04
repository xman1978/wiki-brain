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
