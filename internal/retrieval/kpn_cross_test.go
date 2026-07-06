package retrieval

import (
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

// TestGetKPNNeighbors_IncludesCrossSourceRelations locks in
// docs/impl/v1/kpn.md 步骤 6's claim that KPN expansion needs no code change
// for cross-Source relations: GetKPNNeighbors doesn't filter by scope, so a
// scope=cross relation (from unit's cross-Source matching, module 7)
// participates in expansion exactly like an intra one.
func TestGetKPNNeighbors_IncludesCrossSourceRelations(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	// p1 (s1) and p3 (s2) already have a relation from seedTestData; add a
	// distinct scope=cross pair between p1 (s1) and p4's source (s3) instead
	// via a fresh point to avoid colliding with existing fixture relations.
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u5', 's3', 'cross source topic', 1, 5, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p5', 'u5', 's3', 'cross source content', 'fact')`)
	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope)
		VALUES ('r-cross', 'p1', 'p5', 'related', 'bidirectional', 'v1', 'cross')`)

	neighbors, err := store.GetKPNNeighbors([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, n := range neighbors {
		if n.NeighborPointID == "p5" {
			found = true
			if n.UnitID != "u5" {
				t.Errorf("expected unit_id=u5 for cross-source neighbor, got %s", n.UnitID)
			}
		}
	}
	if !found {
		t.Errorf("expected scope=cross relation to surface as a KPN neighbor, got %+v", neighbors)
	}
}
