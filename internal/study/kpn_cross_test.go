package study

import "testing"

// TestListCrossSourceConflicts covers docs/impl/v1/kpn.md 步骤 5: contradicts
// relations with scope=cross show up in the report's read-only section.
func TestListCrossSourceConflicts(t *testing.T) {
	db := setupTestDB(t)

	seedSource(t, db, "src-a")
	seedSource(t, db, "src-b")
	db.Exec(`UPDATE sources SET title = 'Source A' WHERE source_id = 'src-a'`)
	db.Exec(`UPDATE sources SET title = 'Source B' WHERE source_id = 'src-b'`)
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku-a", "src-a", "con1")
	seedKU(t, db, "ku-b", "src-b", "con1")
	seedKP(t, db, "kp-a", "ku-a", "src-a", "content A")
	seedKP(t, db, "kp-b", "ku-b", "src-b", "content B")

	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope)
		VALUES ('r1', 'kp-a', 'kp-b', 'contradicts', 'bidirectional', 'v1', 'cross')`)
	// An intra-scope contradicts row must not leak into this cross-only section.
	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version, scope)
		VALUES ('r2', 'kp-a', 'kp-b', 'related', 'bidirectional', 'v1', 'intra')`)

	store := NewStore(db)
	conflicts, err := store.ListCrossSourceConflicts(20)
	if err != nil {
		t.Fatalf("ListCrossSourceConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 cross-source conflict, got %d: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.PointA.PointID != "kp-a" || c.PointA.Content != "content A" || c.PointA.SourceTitle != "Source A" {
		t.Errorf("unexpected point_a: %+v", c.PointA)
	}
	if c.PointB.PointID != "kp-b" || c.PointB.Content != "content B" || c.PointB.SourceTitle != "Source B" {
		t.Errorf("unexpected point_b: %+v", c.PointB)
	}
}
