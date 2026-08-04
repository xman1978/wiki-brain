package domain

import (
	"database/sql"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func setupStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := foundation.NewTestDB(t)
	return NewStore(db), db
}

func TestStore_CreateAndList(t *testing.T) {
	store, _ := setupStore(t)

	domainID, err := store.Create("项目管理", "项目全生命周期方法与实践")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if domainID == "" {
		t.Fatal("expected non-empty domain_id")
	}

	domains, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(domains))
	}
	d := domains[0]
	if d.DomainID != domainID || d.Name != "项目管理" || d.Description != "项目全生命周期方法与实践" {
		t.Fatalf("unexpected domain: %+v", d)
	}
	if d.ConceptCount != 0 || d.SourceCount != 0 || d.KPCount != 0 || d.UnassignedKPCount != 0 || d.PendingSignalCount != 0 {
		t.Fatalf("expected all-zero counts for a fresh domain, got %+v", d)
	}
}

func TestStore_List_Counts(t *testing.T) {
	store, db := setupStore(t)

	domainID, err := store.Create("质量管理", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A current, non-merged concept counts; a merged-away one doesn't.
	mustExec(t, db, `INSERT INTO entries (entry_id, domain_id, name) VALUES ('c1', ?, 'concept one')`, domainID)
	mustExec(t, db, `INSERT INTO entries (entry_id, domain_id, name, merged_into) VALUES ('c2', ?, 'concept two', 'c1')`, domainID)

	// A non-shadow source counts; a shadow source doesn't.
	mustExec(t, db, `INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id)
		VALUES ('s1', 'test', 'markdown', 'test.md', '/test.md', '/test.md', 'completed', ?)`, domainID)
	mustExec(t, db, `INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, domain_id, shadow_of)
		VALUES ('s2', 'shadow', 'markdown', 'shadow.md', '/shadow.md', '/shadow.md', 'completed', ?, 's1')`, domainID)

	// A current KU/KP under the counted concept counts toward kp_count.
	mustExec(t, db, `INSERT INTO knowledge_units (unit_id, source_id, entry_id, center, line_start, line_end, status, lifecycle, prompt_version)
		VALUES ('u1', 's1', 'c1', 'topic', 1, 10, 'completed', 'current', 'v1')`)
	mustExec(t, db, `INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type, lifecycle)
		VALUES ('p1', 'u1', 's1', 'test content', 'fact', 'current')`)

	// Unclassified current KP in this domain counts toward unassigned_kp_count;
	// shadow-source and non-current ones do not.
	mustExec(t, db, `INSERT INTO knowledge_units (unit_id, source_id, entry_id, center, line_start, line_end, status, lifecycle, prompt_version)
		VALUES ('u2', 's1', NULL, 'unclassified', 11, 20, 'completed', 'current', 'v1')`)
	mustExec(t, db, `INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type, lifecycle)
		VALUES ('p2', 'u2', 's1', 'unassigned content', 'fact', 'current')`)
	mustExec(t, db, `INSERT INTO knowledge_units (unit_id, source_id, entry_id, center, line_start, line_end, status, lifecycle, prompt_version)
		VALUES ('u3', 's2', NULL, 'shadow unclassified', 1, 5, 'completed', 'current', 'v1')`)
	mustExec(t, db, `INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type, lifecycle)
		VALUES ('p3', 'u3', 's2', 'shadow unassigned', 'fact', 'current')`)

	// A pending_confirm candidate targeting this domain counts; nothing else does.
	mustExec(t, db, `INSERT INTO entry_candidates (candidate_id, kind, domain_id, suggested_name, status, last_signal_at)
		VALUES ('cand1', 'add', ?, 'new concept', 'pending_confirm', CURRENT_TIMESTAMP)`, domainID)
	mustExec(t, db, `INSERT INTO entry_candidates (candidate_id, kind, domain_id, suggested_name, status, last_signal_at)
		VALUES ('cand2', 'add', ?, 'rejected one', 'rejected', CURRENT_TIMESTAMP)`, domainID)

	domains, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(domains))
	}
	d := domains[0]
	if d.ConceptCount != 1 {
		t.Errorf("entry_count = %d, want 1 (merged-away excluded)", d.ConceptCount)
	}
	if d.SourceCount != 1 {
		t.Errorf("source_count = %d, want 1 (shadow excluded)", d.SourceCount)
	}
	if d.KPCount != 1 {
		t.Errorf("kp_count = %d, want 1", d.KPCount)
	}
	if d.UnassignedKPCount != 1 {
		t.Errorf("unassigned_kp_count = %d, want 1 (shadow excluded)", d.UnassignedKPCount)
	}
	if d.PendingSignalCount != 1 {
		t.Errorf("pending_signal_count = %d, want 1 (only pending_confirm counted)", d.PendingSignalCount)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
