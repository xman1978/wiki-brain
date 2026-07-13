package unit

import (
	"testing"
)

// TestApplyOfflineMerge covers 存量治理's confirmed-duplicate merge: survivor
// widens to the cluster's union range, the merged unit's unique point moves
// over (same point_id — traces/links must keep resolving), its redundant
// point stays behind, and the merged unit plus leftovers go superseded.
func TestApplyOfflineMerge(t *testing.T) {
	svc, _, db := setupTestService(t)
	insertSource(t, db, "src-1", "/tmp/unused.md")

	a := &KnowledgeUnit{UnitID: "u-a", SourceID: "src-1", Center: "主题A", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v6"}
	b := &KnowledgeUnit{UnitID: "u-b", SourceID: "src-1", Center: "主题A重复", LineStart: 6, LineEnd: 10, Status: "completed", PromptVersion: "v6"}
	svc.store.InsertUnit(a)
	svc.store.InsertUnit(b)
	svc.store.InsertPoint(&KnowledgePoint{PointID: "p-a1", UnitID: "u-a", SourceID: "src-1", Content: "共同的知识点内容", PointType: "rule"})
	svc.store.InsertPoint(&KnowledgePoint{PointID: "p-b1", UnitID: "u-b", SourceID: "src-1", Content: "共同的知识点内容。", PointType: "rule"})     // 规范化后与 p-a1 等价 — 留在 u-b
	svc.store.InsertPoint(&KnowledgePoint{PointID: "p-b2", UnitID: "u-b", SourceID: "src-1", Content: "只有B有的独有知识点", PointType: "rule"}) // 迁移到 u-a

	if err := svc.ApplyOfflineMerge("u-a", []string{"u-b"}, "merged into u-a (test)"); err != nil {
		t.Fatalf("ApplyOfflineMerge: %v", err)
	}

	survivor, _ := svc.store.GetUnitByID("u-a")
	if survivor.LineStart != 1 || survivor.LineEnd != 10 {
		t.Errorf("survivor range = %d-%d, want 1-10 (union)", survivor.LineStart, survivor.LineEnd)
	}
	if survivor.Lifecycle != LifecycleCurrent {
		t.Errorf("survivor lifecycle = %q, want current", survivor.Lifecycle)
	}

	merged, _ := svc.store.GetUnitByID("u-b")
	if merged.Lifecycle != LifecycleSuperseded {
		t.Errorf("merged unit lifecycle = %q, want superseded", merged.Lifecycle)
	}

	uniq, _ := svc.store.GetPointByID("p-b2")
	if uniq.UnitID != "u-a" {
		t.Errorf("unique point p-b2 unit = %q, want u-a (reparented, same point_id)", uniq.UnitID)
	}
	if uniq.Lifecycle != LifecycleCurrent {
		t.Errorf("unique point lifecycle = %q, want current", uniq.Lifecycle)
	}

	redundant, _ := svc.store.GetPointByID("p-b1")
	if redundant.UnitID != "u-b" {
		t.Errorf("redundant point p-b1 unit = %q, want u-b (stays behind)", redundant.UnitID)
	}
	if redundant.Lifecycle != LifecycleSuperseded {
		t.Errorf("redundant point lifecycle = %q, want superseded (cascaded with its unit)", redundant.Lifecycle)
	}
}

// TestApplyOfflineMerge_RefusesCrossSource pins the safety rail: a confirmed
// pair spanning two sources is a labeling mistake, never a mergeable pair.
func TestApplyOfflineMerge_RefusesCrossSource(t *testing.T) {
	svc, _, db := setupTestService(t)
	insertSource(t, db, "src-1", "/tmp/unused.md")
	insertSource(t, db, "src-2", "/tmp/unused2.md")

	svc.store.InsertUnit(&KnowledgeUnit{UnitID: "u-a", SourceID: "src-1", Center: "主题A", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v6"})
	svc.store.InsertUnit(&KnowledgeUnit{UnitID: "u-x", SourceID: "src-2", Center: "主题A", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v6"})

	if err := svc.ApplyOfflineMerge("u-a", []string{"u-x"}, "test"); err == nil {
		t.Fatal("cross-source merge must be refused")
	}
}
