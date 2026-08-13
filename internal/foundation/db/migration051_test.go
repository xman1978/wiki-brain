package db

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigration051_ActivationBundleMemberConfidence empirically verifies
// migration 051's JSON1 reshape of activation_bundles.member_point_ids from a
// flat point_id array into {point_id,success_count,failure_count,
// last_seen_at} objects, and that fringe_point_ids is dropped
// (docs/impl/v1/activation-bundle.md「成员置信度：Bundle 独有的第二根轴」).
func TestMigration051_ActivationBundleMemberConfidence(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:?_journal_mode=WAL&_foreign_keys=on")
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
		if strings.HasPrefix(name, "051_") {
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
		t.Fatal("051_activation_bundle_member_confidence.sql not found in embedded migrations")
	}

	if _, err := database.Exec(`INSERT INTO activation_bundles
		(bundle_id, cluster_fingerprint, representative_terms, observed_conditions, member_point_ids, fringe_point_ids, status, created_from)
		VALUES ('b1', 'fp1', 's1 i1', '[]', '["kp1","kp2"]', '["kp3"]', 'candidate', '[]')`); err != nil {
		t.Fatalf("seed legacy bundle: %v", err)
	}

	content, err := migrationsFS.ReadFile("migrations/" + target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if _, err := database.Exec(string(content)); err != nil {
		t.Fatalf("apply migration 051: %v", err)
	}

	// fringe_point_ids column must be gone.
	rows, err := database.Query(`PRAGMA table_info(activation_bundles)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	hasFringeCol := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "fringe_point_ids" {
			hasFringeCol = true
		}
	}
	rows.Close()
	if hasFringeCol {
		t.Error("expected fringe_point_ids column to be dropped from activation_bundles")
	}

	var memberRaw string
	if err := database.QueryRow(`SELECT member_point_ids FROM activation_bundles WHERE bundle_id = 'b1'`).Scan(&memberRaw); err != nil {
		t.Fatalf("query member_point_ids: %v", err)
	}
	var members []map[string]interface{}
	if err := json.Unmarshal([]byte(memberRaw), &members); err != nil {
		t.Fatalf("unmarshal member_point_ids: %v (raw=%s)", err, memberRaw)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d (raw=%s)", len(members), memberRaw)
	}
	seen := map[string]bool{}
	for _, m := range members {
		pid, _ := m["point_id"].(string)
		seen[pid] = true
		sc, ok := m["success_count"].(float64)
		if !ok || sc != 1 {
			t.Errorf("point %s success_count = %v, want 1", pid, m["success_count"])
		}
		fc, ok := m["failure_count"].(float64)
		if !ok || fc != 0 {
			t.Errorf("point %s failure_count = %v, want 0", pid, m["failure_count"])
		}
		if _, ok := m["last_seen_at"].(string); !ok {
			t.Errorf("point %s last_seen_at missing or not a string, raw=%s", pid, memberRaw)
		}
	}
	if !seen["kp1"] || !seen["kp2"] {
		t.Errorf("expected kp1 and kp2 among members, raw=%s", memberRaw)
	}
}
