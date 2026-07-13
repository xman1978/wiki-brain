package retrieval

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func insertRetrievalTestSourceAndUnit(t *testing.T, db *sql.DB, sourceID, unitID string) {
	t.Helper()
	_, err := db.Exec(`INSERT OR IGNORE INTO sources
		(source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES (?, ?, 'md', ?, '/tmp/source.md', '/tmp/source.md', 'completed')`,
		sourceID, sourceID, sourceID+".md")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO knowledge_units
		(unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, 'test unit', 1, 1, 'completed', 'v1')`, unitID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetUnitRerankSemanticsBulk(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u1")
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u2")
	_, err := db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES ('u1', '制度', '报销', '说明限额', '员工', '出差', '["住宿限额500元"]', 'v1')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES ('u2', '制度', '报销', '说明流程', '员工', '出差', '["提交发票"]', 'v1')`)
	if err != nil {
		t.Fatal(err)
	}

	got, err := NewStore(db).GetUnitRerankSemantics([]string{"u1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["u1"].KeyFacts, []string{"住宿限额500元"}) {
		t.Fatalf("key facts = %#v, want 住宿限额500元", got["u1"].KeyFacts)
	}
	if _, ok := got["u2"]; ok {
		t.Fatal("unrequested unit returned")
	}
}

func TestGetUnitRerankSemanticsMalformedFacts(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u1")
	if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES ('u1', '制度', '报销', '说明限额', '员工', '出差', '{', 'v1')`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewStore(db).GetUnitRerankSemantics([]string{"u1"})
	if err == nil {
		t.Fatal("expected malformed key facts error")
	}
	if !strings.Contains(err.Error(), "u1") {
		t.Fatalf("error %q does not name unit u1", err)
	}
}

func TestUnitRerankSemanticsMigrationRequiresFactsArray(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u1")
	for _, factsJSON := range []string{"null", `{}`, `"fact"`} {
		_, err := db.Exec(`INSERT INTO unit_rerank_semantics
			(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
			VALUES ('u1', '制度', '报销', '说明限额', '员工', '出差', ?, 'v1')`, factsJSON)
		if err == nil {
			t.Fatalf("key_facts_json %s passed migration CHECK, want rejection", factsJSON)
		}
	}
}

func TestGetUnitRerankSemanticsRejectsInvalidFactsShape(t *testing.T) {
	tests := []struct {
		name      string
		factsJSON string
	}{
		{name: "null", factsJSON: "null"},
		{name: "object", factsJSON: `{}`},
		{name: "non-string element", factsJSON: `["valid", 7]`},
		{name: "null element", factsJSON: `[null]`},
		{name: "null after valid element", factsJSON: `["valid", null]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := foundation.NewTestDB(t)
			insertRetrievalTestSourceAndUnit(t, db, "s1", "u1")
			if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatal(err)
			}
			_, err := db.Exec(`INSERT INTO unit_rerank_semantics
				(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
				VALUES ('u1', '制度', '报销', '说明限额', '员工', '出差', ?, 'v1')`, tc.factsJSON)
			if err != nil {
				t.Fatal(err)
			}

			_, err = NewStore(db).GetUnitRerankSemantics([]string{"u1"})
			if err == nil || !strings.Contains(err.Error(), "u1") {
				t.Fatalf("GetUnitRerankSemantics err = %v, want unit-scoped decode rejection", err)
			}
		})
	}
}

func seedTestData(t *testing.T, store *Store) {
	t.Helper()
	db := store.db

	db.Exec(`INSERT INTO domains (domain_id, name, description) VALUES ('d1', 'Math', 'Mathematics')`)
	db.Exec(`INSERT INTO domains (domain_id, name, description) VALUES ('d2', 'Physics', 'Physics')`)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES ('s1', 'Algebra', 'md', 'algebra.md', '/tmp/algebra.md', '/tmp/algebra.md', 'completed', 'd1')`)
	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES ('s2', 'Mechanics', 'md', 'mech.md', '/tmp/mech.md', '/tmp/mech.md', 'completed', 'd2')`)
	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, summary)
		VALUES ('s3', 'General', 'md', 'gen.md', '/tmp/gen.md', '/tmp/gen.md', 'completed', 'General notes')`)

	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('o1', 's1', 1, 'Chapter 1', 1, 50, 'section', 1)`)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, line_start, line_end, node_type, position)
		VALUES ('o2', 's1', 'o1', 2, 'Section 1.1', 1, 25, 'section', 1)`)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, line_start, line_end, node_type, position)
		VALUES ('o3', 's1', 'o1', 2, 'Section 1.2', 26, 50, 'section', 2)`)

	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, outline_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u1', 's1', 'o2', 'linear equations', 1, 25, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, outline_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u2', 's1', 'o3', 'quadratic equations', 26, 50, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u3', 's2', 'newton laws', 1, 30, 'completed', 'v1')`)

	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p1', 'u1', 's1', 'ax+b=0 is linear', 'fact')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p2', 'u2', 's1', 'ax^2+bx+c=0 is quadratic', 'fact')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p3', 'u3', 's2', 'F=ma', 'fact')`)

	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version)
		VALUES ('r1', 'p1', 'p2', 'related', 'bidirectional', 'v1')`)
	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version)
		VALUES ('r2', 'p1', 'p3', 'supplements', 'directed', 'v1')`)

	// p4 contradicts p1
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u4', 's3', 'updated equations', 1, 10, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p4', 'u4', 's3', 'ax+b=0 has been deprecated', 'fact')`)
	db.Exec(`INSERT INTO knowledge_point_relations (relation_id, source_point_id, target_point_id, relation_type, direction, prompt_version)
		VALUES ('r3', 'p1', 'p4', 'contradicts', 'bidirectional', 'v1')`)
}

func TestListDomains(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	domains, err := store.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(domains))
	}
}

func TestListSourcesByDomainIDs(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	sources, err := store.ListSourcesByDomainIDs([]string{"d1"})
	if err != nil {
		t.Fatal(err)
	}
	// s1 (domain d1) + s3 (domain null)
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	// empty domain_ids returns all
	all, err := store.ListSourcesByDomainIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(all))
	}
}

func TestGetOutlinesBySourceIDs(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	outlines, err := store.GetOutlinesBySourceIDs([]string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outlines) != 3 {
		t.Fatalf("expected 3 outlines, got %d", len(outlines))
	}
}

func TestGetUnitsByOutlineIDs(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	units, err := store.GetUnitsByOutlineIDs([]string{"o2", "o3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("expected 2 units, got %d", len(units))
	}
}

func TestGetFirstPointByUnitID(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	pid, err := store.GetFirstPointByUnitID("u1")
	if err != nil {
		t.Fatal(err)
	}
	if pid != "p1" {
		t.Fatalf("expected p1, got %s", pid)
	}
}

func TestGetKPNNeighbors(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	// p1→p2 related bidirectional → p2 is neighbor
	// p1→p3 supplements directed → p3 is neighbor
	// p1→p4 contradicts → excluded from GetKPNNeighbors
	neighbors, err := store.GetKPNNeighbors([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 2 {
		t.Fatalf("expected 2 neighbors (contradicts excluded), got %d", len(neighbors))
	}

	// p2 as seed: bidirectional related with p1 → p1 is neighbor
	neighbors2, err := store.GetKPNNeighbors([]string{"p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors2) != 1 {
		t.Fatalf("expected 1 neighbor for p2, got %d", len(neighbors2))
	}

	// p3 as seed: directed r2 is p1→p3, p3 is target, direction=directed
	// so p3 cannot reverse-trace to p1
	neighbors3, err := store.GetKPNNeighbors([]string{"p3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors3) != 0 {
		t.Fatalf("expected 0 neighbors for p3, got %d", len(neighbors3))
	}
}

func TestGetKPNConflicts(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	// p1→p4 contradicts bidirectional
	conflicts, err := store.GetKPNConflicts([]string{"p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].NeighborPointID != "p4" {
		t.Errorf("expected conflict with p4, got %s", conflicts[0].NeighborPointID)
	}
	if conflicts[0].UnitID != "u4" {
		t.Errorf("expected unit u4, got %s", conflicts[0].UnitID)
	}

	// reverse direction: p4 as seed should also find p1
	conflicts2, err := store.GetKPNConflicts([]string{"p4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts2) != 1 {
		t.Fatalf("expected 1 reverse conflict, got %d", len(conflicts2))
	}
	if conflicts2[0].NeighborPointID != "p1" {
		t.Errorf("expected reverse conflict with p1, got %s", conflicts2[0].NeighborPointID)
	}

	// p2 has no contradicts
	conflicts3, err := store.GetKPNConflicts([]string{"p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts3) != 0 {
		t.Fatalf("expected 0 conflicts for p2, got %d", len(conflicts3))
	}
}

func TestGetChildOutlineIDs(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	ids, err := store.GetChildOutlineIDs([]string{"o1"}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	// o1, o2, o3
	if len(ids) != 3 {
		t.Fatalf("expected 3 outline ids, got %d", len(ids))
	}
}
