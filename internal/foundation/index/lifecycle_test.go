package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/jxman78/wiki-brain/internal/foundation"
)

// TestLifecycle_IndexSearchCorruptRebuild verifies the full lifecycle:
//
//  1. Import: index documents from DB data
//  2. Tokenize: gse produces meaningful Chinese tokens
//  3. Search: MatchQuery finds indexed Chinese content
//  4. Reopen: close and reopen — search still works
//  5. Corrupt: delete index files, simulate damage
//  6. EnsureHealthy: detects corruption and rebuilds
//  7. Search again: rebuilt index returns same results
func TestLifecycle_IndexSearchCorruptRebuild(t *testing.T) {
	db := foundation.NewTestDB(t)
	idxDir := filepath.Join(t.TempDir(), "idx")
	mdDir := filepath.Join(t.TempDir(), "md")
	os.MkdirAll(mdDir, 0755)

	// Prepare DB data and markdown files
	mdContent := "# 差旅费报销制度\n\n住宿费用采用限额实报实销制。\n\n出差期间乘坐飞机需提前审批。\n"
	mdPath := filepath.Join(mdDir, "s1.md")
	os.WriteFile(mdPath, []byte(mdContent), 0644)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('s1', '差旅费报销制度', 'md', 's1.md', ?, ?, 'completed')`, mdPath, mdPath)
	db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, summary, line_start, line_end, node_type, position)
		VALUES ('o1', 's1', 1, '差旅费报销制度', '住宿费 飞机 审批', 1, 5, 'structural', 1)`)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version)
		VALUES ('u1', 's1', '差旅费报销', 1, 5, 'completed', 'v1')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p1', 'u1', 's1', '住宿费用采用限额实报实销制', 'fact')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES ('p2', 'u1', 's1', '出差期间乘坐飞机需提前审批', 'rule')`)

	readMD := func(sourceID string) ([]string, error) {
		data, err := os.ReadFile(filepath.Join(mdDir, sourceID+".md"))
		if err != nil {
			return nil, err
		}
		return strings.Split(string(data), "\n"), nil
	}

	// ---- Phase 1: Create + Rebuild (simulates import) ----
	mgr, err := NewManager(idxDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	stats, err := mgr.Rebuild(db, readMD)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Units != 1 || stats.Points != 2 || stats.Outlines != 1 {
		t.Fatalf("Rebuild stats: units=%d points=%d outlines=%d", stats.Units, stats.Points, stats.Outlines)
	}

	t.Logf("Phase 1 OK: Rebuild stats units=%d points=%d outlines=%d", stats.Units, stats.Points, stats.Outlines)

	// ---- Phase 2: Verify tokenizer works ----
	analyzer := mgr.Units.Mapping().AnalyzerNamed("wiki_brain")
	if analyzer == nil {
		t.Fatal("wiki_brain analyzer not found")
	}
	tokens := analyzer.Analyze([]byte("住宿费用"))
	if len(tokens) == 0 {
		t.Fatal("gse tokenizer produced zero tokens for '住宿费用'")
	}
	t.Logf("Phase 2 OK: Tokens for '住宿费用': %v", tokenTerms(tokens))

	// ---- Phase 3: Search works ----
	assertSearch(t, mgr.Units, "住宿", true, "units/住宿")
	assertSearch(t, mgr.Units, "报销", true, "units/报销")
	assertSearch(t, mgr.Units, "飞机", true, "units/飞机")
	assertSearch(t, mgr.Points, "住宿", true, "points/住宿")
	assertSearch(t, mgr.Points, "飞机", true, "points/飞机")
	assertSearch(t, mgr.Outlines, "报销", true, "outlines/报销")
	assertSearch(t, mgr.Units, "量子力学", false, "units/量子力学 (should miss)")

	// ---- Phase 4: Reopen and search ----
	mgr.Close()
	mgr2, err := NewManager(idxDir)
	if err != nil {
		t.Fatalf("reopen NewManager: %v", err)
	}
	assertSearch(t, mgr2.Units, "住宿", true, "reopened units/住宿")
	assertSearch(t, mgr2.Points, "飞机", true, "reopened points/飞机")

	// ---- Phase 5: Health check should pass ----
	status := mgr2.CheckHealth(db)
	if !status.Healthy {
		t.Fatalf("health check failed on good index: %v", status.Issues)
	}
	mgr2.Close()

	// ---- Phase 6: Corrupt the index (delete files) ----
	os.RemoveAll(filepath.Join(idxDir, "units"))
	os.RemoveAll(filepath.Join(idxDir, "points"))

	// ---- Phase 7: EnsureHealthy detects and rebuilds ----
	mgr3, err := EnsureHealthy(idxDir, db, readMD)
	if err != nil {
		t.Fatalf("EnsureHealthy after corruption: %v", err)
	}
	defer mgr3.Close()

	// ---- Phase 8: Rebuilt index works ----
	assertSearch(t, mgr3.Units, "住宿", true, "rebuilt units/住宿")
	assertSearch(t, mgr3.Units, "飞机", true, "rebuilt units/飞机")
	assertSearch(t, mgr3.Points, "住宿", true, "rebuilt points/住宿")
	assertSearch(t, mgr3.Outlines, "报销", true, "rebuilt outlines/报销")
	assertSearch(t, mgr3.Units, "量子力学", false, "rebuilt units/量子力学 (should miss)")

	// Verify doc counts
	uCount, _ := mgr3.Units.DocCount()
	pCount, _ := mgr3.Points.DocCount()
	oCount, _ := mgr3.Outlines.DocCount()
	if uCount != 1 || pCount != 2 || oCount != 1 {
		t.Errorf("rebuilt doc counts: units=%d points=%d outlines=%d (want 1,2,1)", uCount, pCount, oCount)
	}

	// Final health check on rebuilt index
	status2 := mgr3.CheckHealth(db)
	if !status2.Healthy {
		t.Fatalf("rebuilt index health check failed: %v", status2.Issues)
	}
}

// TestLifecycle_KeywordFieldSurvivesTermQuery is a regression test for the
// bug where `lifecycle` fell under DefaultAnalyzer (gse tokenizer) instead of
// a keyword mapping: TermQuery("current") searches the raw term without
// running it through gse, so it never matched whatever gse had produced at
// index time, and lifecycleCurrentQuery's conjunction always returned zero
// hits — for every query, not just this project's specific test cases.
func TestLifecycle_KeywordFieldSurvivesTermQuery(t *testing.T) {
	db := foundation.NewTestDB(t)
	idxDir := filepath.Join(t.TempDir(), "idx")
	mdDir := filepath.Join(t.TempDir(), "md")
	os.MkdirAll(mdDir, 0755)

	mdContent := "# 集群维护\n\n重启集群需要先停止再启动。\n"
	mdPath := filepath.Join(mdDir, "s1.md")
	os.WriteFile(mdPath, []byte(mdContent), 0644)

	db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('s1', '集群维护', 'md', 's1.md', ?, ?, 'completed')`, mdPath, mdPath)
	db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, center, line_start, line_end, status, prompt_version, lifecycle)
		VALUES ('u1', 's1', '集群重启', 1, 3, 'completed', 'v1', 'current')`)
	db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type, lifecycle)
		VALUES ('p1', 'u1', 's1', '重启集群需要先停止再启动', 'method', 'current')`)

	readMD := func(sourceID string) ([]string, error) {
		data, err := os.ReadFile(filepath.Join(mdDir, sourceID+".md"))
		if err != nil {
			return nil, err
		}
		return strings.Split(string(data), "\n"), nil
	}

	mgr, err := NewManager(idxDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	if _, err := mgr.Rebuild(db, readMD); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	lifecycleOnly := bleve.NewTermQuery("current")
	lifecycleOnly.SetField("lifecycle")
	req := bleve.NewSearchRequest(lifecycleOnly)
	res, err := mgr.Units.Search(req)
	if err != nil {
		t.Fatalf("lifecycle term query: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("TermQuery(lifecycle=current) matched 0 units — keyword field mapping regressed")
	}

	conj := bleve.NewConjunctionQuery(bleve.NewMatchQuery("重启"), lifecycleOnly)
	req2 := bleve.NewSearchRequest(conj)
	res2, err := mgr.Units.Search(req2)
	if err != nil {
		t.Fatalf("conjunction query: %v", err)
	}
	if res2.Total == 0 {
		t.Fatal("MatchQuery(重启) AND TermQuery(lifecycle=current) matched 0 units — this is the exact retrieval.ftsRecall failure mode")
	}
}

func assertSearch(t *testing.T, idx bleve.Index, query string, expectHit bool, label string) {
	t.Helper()
	q := bleve.NewMatchQuery(query)
	req := bleve.NewSearchRequest(q)
	req.Size = 1
	res, err := idx.Search(req)
	if err != nil {
		t.Fatalf("%s: search error: %v", label, err)
	}
	if expectHit && res.Total == 0 {
		t.Errorf("%s: expected hit but got 0 results", label)
	}
	if !expectHit && res.Total > 0 {
		t.Errorf("%s: expected no hit but got %d results", label, res.Total)
	}
}

func tokenTerms(tokens []*analysis.Token) []string {
	terms := make([]string, len(tokens))
	for i, tok := range tokens {
		terms[i] = string(tok.Term)
	}
	return terms
}
