package db

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration050_ActivationConfidenceConvergence is the Store-level
// integration test the execution plan calls out as the single highest-risk
// piece of this phase: it seeds pre-migration-shaped observed_conditions
// JSON (hit_count, plus the table-level known_question_terms column) into a
// fresh DB that has every migration up to (not including) 050 applied, runs
// 050, and asserts the post-migration shape — empirically confirming
// the sqlite driver's JSON1 build supports json_set/json_remove/
// json_group_array/json_each the way this migration relies on
// (docs/impl/v1/activation.md「Migration 与回填」).
func TestMigration050_ActivationConfidenceConvergence(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var target string
	for _, name := range files {
		if strings.HasPrefix(name, "050_") {
			target = name
			break
		}
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := database.Exec(string(content)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	if target == "" {
		t.Fatal("050_activation_confidence_convergence.sql not found in embedded migrations")
	}

	// Seed a pre-050-shaped row: observed_conditions with hit_count (no
	// success/failure/audited_* fields), and a non-empty table-level
	// known_question_terms.
	legacyConds := `[{"subject":"s1","intent":"i1","audience":"a1","constraint":"c1","question_terms":"q1","first_seen_at":"2026-01-01T00:00:00Z","last_seen_at":"2026-01-02T00:00:00Z","hit_count":7}]`
	legacyKnown := `["q1","q2"]`
	if _, err := database.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('ku1', 'src1', 'topic', 1, 10, 'done', 'v1')`); err != nil {
		// sources FK — seed source first if the KU insert failed on FK.
		if _, serr := database.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
			VALUES ('src1', 't', 'markdown', 'f.md', '/f.md', '/f.md', 'done')`); serr != nil {
			t.Fatalf("seed source: %v", serr)
		}
		if _, err := database.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
			VALUES ('ku1', 'src1', 'topic', 1, 10, 'done', 'v1')`); err != nil {
			t.Fatalf("seed ku: %v", err)
		}
	}
	if _, err := database.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('kp1', 'ku1', 'src1', 'content', 'fact')`); err != nil {
		t.Fatalf("seed kp: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO activation_links
		(link_id, question_terms, subject_terms, intent_terms, audience, constraint_terms,
		 observed_conditions, known_question_terms, point_id, status, created_from)
		VALUES ('l1', 'q1', 's1', '[]', '[]', '[]', ?, ?, 'kp1', 'candidate', '[]')`,
		legacyConds, legacyKnown); err != nil {
		t.Fatalf("seed legacy link: %v", err)
	}

	// Also seed an activation_bundles row (migration 048) with a legacy
	// hit_count-shaped condition.
	bundleConds := `[{"subject":"s1","intent":"i1","audience":"a1","constraint":"c1","first_seen_at":"2026-01-01T00:00:00Z","last_seen_at":"2026-01-02T00:00:00Z","hit_count":3}]`
	if _, err := database.Exec(`INSERT INTO activation_bundles
		(bundle_id, cluster_fingerprint, representative_terms, observed_conditions, member_point_ids, fringe_point_ids, status, created_from)
		VALUES ('b1', 'fp1', 's1 i1', ?, '["kp1"]', '[]', 'candidate', '[]')`, bundleConds); err != nil {
		t.Fatalf("seed legacy bundle: %v", err)
	}

	content, err := migrationsFS.ReadFile("migrations/" + target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if _, err := database.Exec(string(content)); err != nil {
		t.Fatalf("apply migration 050: %v", err)
	}

	// activation_links: known_question_terms column must be gone.
	rows, err := database.Query(`PRAGMA table_info(activation_links)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	hasKnownCol := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "known_question_terms" {
			hasKnownCol = true
		}
	}
	rows.Close()
	if hasKnownCol {
		t.Error("expected known_question_terms column to be dropped from activation_links")
	}

	var observedRaw string
	if err := database.QueryRow(`SELECT observed_conditions FROM activation_links WHERE link_id = 'l1'`).Scan(&observedRaw); err != nil {
		t.Fatalf("query observed_conditions: %v", err)
	}
	var conds []map[string]interface{}
	if err := json.Unmarshal([]byte(observedRaw), &conds); err != nil {
		t.Fatalf("unmarshal observed_conditions: %v (raw=%s)", err, observedRaw)
	}
	if len(conds) != 1 {
		t.Fatalf("expected 1 condition, got %d (raw=%s)", len(conds), observedRaw)
	}
	c := conds[0]
	if _, ok := c["hit_count"]; ok {
		t.Errorf("hit_count should have been removed, raw=%s", observedRaw)
	}
	if sc, ok := c["success_count"].(float64); !ok || sc != 7 {
		t.Errorf("success_count = %v, want 7 (from hit_count), raw=%s", c["success_count"], observedRaw)
	}
	if fc, ok := c["failure_count"].(float64); !ok || fc != 0 {
		t.Errorf("failure_count = %v, want 0, raw=%s", c["failure_count"], observedRaw)
	}
	if asc, ok := c["audited_success_count"].(float64); !ok || asc != 0 {
		t.Errorf("audited_success_count = %v, want 0, raw=%s", c["audited_success_count"], observedRaw)
	}
	if afc, ok := c["audited_failure_count"].(float64); !ok || afc != 0 {
		t.Errorf("audited_failure_count = %v, want 0, raw=%s", c["audited_failure_count"], observedRaw)
	}
	kqt, ok := c["known_question_terms"].([]interface{})
	if !ok || len(kqt) != 2 {
		t.Errorf("known_question_terms = %v, want the 2 folded-in table-level terms, raw=%s", c["known_question_terms"], observedRaw)
	}

	// activation_bundles: same JSON reshape, no known_question_terms fold (no
	// table-level column existed on bundles).
	var bundleRaw string
	if err := database.QueryRow(`SELECT observed_conditions FROM activation_bundles WHERE bundle_id = 'b1'`).Scan(&bundleRaw); err != nil {
		t.Fatalf("query bundle observed_conditions: %v", err)
	}
	var bconds []map[string]interface{}
	if err := json.Unmarshal([]byte(bundleRaw), &bconds); err != nil {
		t.Fatalf("unmarshal bundle observed_conditions: %v (raw=%s)", err, bundleRaw)
	}
	if len(bconds) != 1 {
		t.Fatalf("expected 1 bundle condition, got %d", len(bconds))
	}
	if sc, ok := bconds[0]["success_count"].(float64); !ok || sc != 3 {
		t.Errorf("bundle success_count = %v, want 3", bconds[0]["success_count"])
	}
}
