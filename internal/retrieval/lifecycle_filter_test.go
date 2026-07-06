package retrieval

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

// These tests cover docs/impl/v1/retrieval.md 步骤 5 (lifecycle filtering) at
// the store layer, using the same fixture as store_test.go's seedTestData:
// u1/p1 (s1), u2/p2 (s1), u3/p3 (s2), u4/p4 (s3, contradicts p1).

func TestGetUnitsByOutlineIDs_ExcludesNonCurrent(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'u1'`)

	units, err := store.GetUnitsByOutlineIDs([]string{"o2", "o3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].UnitID != "u2" {
		t.Fatalf("expected only u2 (u1 superseded), got %+v", units)
	}
}

func TestGetFirstPointByUnitID_ExcludesNonCurrent(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_points SET lifecycle = 'deprecated' WHERE point_id = 'p1'`)

	if _, err := store.GetFirstPointByUnitID("u1"); err == nil {
		t.Error("expected error (no current point) once p1 is deprecated")
	}
}

func TestGetPointUnitID_ExcludesNonCurrent(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'p1'`)

	if _, err := store.GetPointUnitID("p1"); err == nil {
		t.Error("expected error once p1 is superseded")
	}
}

func TestGetKPNNeighbors_ExcludesNonCurrentKP(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'p2'`)

	neighbors, err := store.GetKPNNeighbors([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	// p1's neighbors are p2 (superseded, excluded) and p3 (current).
	if len(neighbors) != 1 || neighbors[0].NeighborPointID != "p3" {
		t.Fatalf("expected only p3, got %+v", neighbors)
	}
}

func TestGetKPNNeighbors_ExcludesNonCurrentKU(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_units SET lifecycle = 'deprecated' WHERE unit_id = 'u2'`)

	neighbors, err := store.GetKPNNeighbors([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 1 || neighbors[0].NeighborPointID != "p3" {
		t.Fatalf("expected only p3 (p2's KU deprecated), got %+v", neighbors)
	}
}

func TestGetKPNConflicts_ExcludesNonCurrent(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_points SET lifecycle = 'deprecated' WHERE point_id = 'p4'`)

	conflicts, err := store.GetKPNConflicts([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts once p4 is deprecated, got %+v", conflicts)
	}
}

func TestGetCurrentUnitsByPointIDs(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	hits, err := store.GetCurrentUnitsByPointIDs([]string{"p1", "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}

	db.Exec(`UPDATE knowledge_points SET lifecycle = 'superseded' WHERE point_id = 'p1'`)
	hits2, err := store.GetCurrentUnitsByPointIDs([]string{"p1", "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits2) != 1 || hits2[0].PointID != "p2" {
		t.Fatalf("expected only p2 once p1 superseded, got %+v", hits2)
	}
}

func TestGetCurrentUnitsByPointIDs_ExcludesNonCurrentKU(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`UPDATE knowledge_units SET lifecycle = 'deprecated' WHERE unit_id = 'u1'`)

	hits, err := store.GetCurrentUnitsByPointIDs([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits once p1's KU is deprecated, got %+v", hits)
	}
}

func TestUnitLifecycleCurrent(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	current, err := store.UnitLifecycleCurrent("u1")
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Error("expected u1 to be current")
	}

	db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'u1'`)
	current2, err := store.UnitLifecycleCurrent("u1")
	if err != nil {
		t.Fatal(err)
	}
	if current2 {
		t.Error("expected u1 to no longer be current")
	}
}

func TestListAllSources_ExcludesShadow(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, shadow_of)
		VALUES ('shadow1', 'Algebra (reupload)', 'md', 'algebra.md', '/tmp/a.md', '/tmp/a.md', 'processing', 's1')`)

	sources, err := store.ListAllSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		if s.SourceID == "shadow1" {
			t.Error("expected shadow source excluded from ListAllSources")
		}
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 non-shadow sources, got %d", len(sources))
	}
}

func TestListSourcesByDomainIDs_ExcludesShadow(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id, shadow_of)
		VALUES ('shadow1', 'Algebra (reupload)', 'md', 'algebra.md', '/tmp/a.md', '/tmp/a.md', 'processing', 'd1', 's1')`)

	sources, err := store.ListSourcesByDomainIDs([]string{"d1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		if s.SourceID == "shadow1" {
			t.Error("expected shadow source excluded from ListSourcesByDomainIDs")
		}
	}
}

func TestFilterCurrentUnits(t *testing.T) {
	svc, _, _ := setupTestService(t)

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25},
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50},
	}

	kept := svc.filterCurrentUnits(candidates)
	if len(kept) != 2 {
		t.Fatalf("expected 2 kept before lifecycle change, got %d", len(kept))
	}

	svc.store.db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'u1'`)
	kept2 := svc.filterCurrentUnits(candidates)
	if len(kept2) != 1 || kept2[0].unitID != "u2" {
		t.Fatalf("expected only u2 after u1 superseded, got %+v", kept2)
	}
}
