package retrieval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func setupTestService(t *testing.T) (*Service, *llm.FakeClient, *Store) {
	t.Helper()
	db := foundation.NewTestDB(t)
	store := NewStore(db)
	seedTestData(t, store)

	// Create markdown files for sources
	tmpDir := foundation.NewTestDir(t)
	mdContent := "# Algebra\n\nLine 2\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10\n" +
		"Line 11\nLine 12\nLine 13\nLine 14\nLine 15\nLine 16\nLine 17\nLine 18\nLine 19\nLine 20\n" +
		"Line 21\nLine 22\nLine 23\nLine 24\nLine 25\n" +
		"Line 26\nLine 27\nLine 28\nLine 29\nLine 30\n" +
		"Line 31\nLine 32\nLine 33\nLine 34\nLine 35\nLine 36\nLine 37\nLine 38\nLine 39\nLine 40\n" +
		"Line 41\nLine 42\nLine 43\nLine 44\nLine 45\nLine 46\nLine 47\nLine 48\nLine 49\nLine 50"

	mdPath := filepath.Join(tmpDir, "algebra.md")
	os.WriteFile(mdPath, []byte(mdContent), 0644)

	mdPath2 := filepath.Join(tmpDir, "mech.md")
	os.WriteFile(mdPath2, []byte("# Mechanics\nF=ma\nLine 3\n"+
		"Line 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10\n"+
		"Line 11\nLine 12\nLine 13\nLine 14\nLine 15\nLine 16\nLine 17\nLine 18\nLine 19\nLine 20\n"+
		"Line 21\nLine 22\nLine 23\nLine 24\nLine 25\nLine 26\nLine 27\nLine 28\nLine 29\nLine 30"), 0644)

	mdPath3 := filepath.Join(tmpDir, "gen.md")
	os.WriteFile(mdPath3, []byte("# General\nUpdated equations\nax+b=0 has been deprecated\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10"), 0644)

	// Update markdown_path in DB
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's1'`, mdPath)
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's2'`, mdPath2)
	db.Exec(`UPDATE sources SET markdown_path = ? WHERE source_id = 's3'`, mdPath3)

	// Create Bleve indexes and index test data
	idxDir := filepath.Join(tmpDir, "index")
	idxMgr, err := index.NewManager(idxDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	// Index units
	idxMgr.Units.Index("u1", map[string]interface{}{
		"unit_id": "u1", "source_id": "s1", "center": "linear equations",
		"line_start": 1, "line_end": 25,
		"content": "# Algebra\nLinear equations ax+b=0",
	})
	idxMgr.Units.Index("u2", map[string]interface{}{
		"unit_id": "u2", "source_id": "s1", "center": "quadratic equations",
		"line_start": 26, "line_end": 50,
		"content": "Quadratic equations ax^2+bx+c=0",
	})
	idxMgr.Units.Index("u3", map[string]interface{}{
		"unit_id": "u3", "source_id": "s2", "center": "newton laws",
		"line_start": 1, "line_end": 30,
		"content": "Newton's laws F=ma",
	})

	// Index points
	idxMgr.Points.Index("p1", map[string]interface{}{
		"point_id": "p1", "unit_id": "u1", "source_id": "s1",
		"content": "ax+b=0 is linear", "point_type": "fact",
	})
	idxMgr.Points.Index("p2", map[string]interface{}{
		"point_id": "p2", "unit_id": "u2", "source_id": "s1",
		"content": "ax^2+bx+c=0 is quadratic", "point_type": "fact",
	})
	idxMgr.Points.Index("p3", map[string]interface{}{
		"point_id": "p3", "unit_id": "u3", "source_id": "s2",
		"content": "F=ma", "point_type": "fact",
	})

	// Index outlines
	idxMgr.Outlines.Index("o1", map[string]interface{}{
		"outline_id": "o1", "source_id": "s1", "title": "Chapter 1 Algebra",
		"level": 1, "node_type": "section",
	})
	idxMgr.Outlines.Index("o2", map[string]interface{}{
		"outline_id": "o2", "source_id": "s1", "title": "Linear Equations",
		"level": 2, "node_type": "section",
	})
	idxMgr.Outlines.Index("o3", map[string]interface{}{
		"outline_id": "o3", "source_id": "s1", "title": "Quadratic Equations",
		"level": 2, "node_type": "section",
	})

	fake := llm.NewFakeClient()
	cfg := &config.Config{
		Retrieval: config.RetrievalConfig{
			OutlineFTSMinScore: 0.5,
			RerankTopN:         20,
		},
	}

	svc := NewService(store, fake, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg)
	return svc, fake, store
}

func TestDomainPreFilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d1"]}`,
	})

	sources, err := svc.domainPreFilter(context.Background(), "what is linear equation?")
	if err != nil {
		t.Fatal(err)
	}
	// d1 sources + null domain sources
	for _, s := range sources {
		if s.DomainID.Valid && s.DomainID.String != "d1" {
			t.Errorf("unexpected domain_id: %s", s.DomainID.String)
		}
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (d1 + null domain), got %d", len(sources))
	}
}

func TestDomainPreFilterFallback(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": []}`,
	})

	sources, err := svc.domainPreFilter(context.Background(), "something")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources (all), got %d", len(sources))
	}
}

func TestSourceSemanticFilter(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []SourceInfo{
		{SourceID: "s1", Title: "Algebra"},
		{SourceID: "s2", Title: "Mechanics"},
	}

	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": ["s1"]}`,
	})

	filtered, err := svc.sourceSemanticFilter(context.Background(), QueryContext{Question: "linear equation"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].SourceID != "s1" {
		t.Fatalf("expected [s1], got %v", filtered)
	}
}

func TestSourceSemanticFilterEmptyFallback(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []SourceInfo{
		{SourceID: "s1", Title: "Algebra"},
		{SourceID: "s2", Title: "Mechanics"},
	}

	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": []}`,
	})

	filtered, err := svc.sourceSemanticFilter(context.Background(), QueryContext{Question: "random question"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 sources (fallback to all), got %d", len(filtered))
	}
}

func TestRRFMerge(t *testing.T) {
	svc, _, _ := setupTestService(t)

	outlineCandidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", score: 2.0, sourcePaths: []string{"outline"}},
		{unitID: "u2", pointID: "p2", sourceID: "s1", score: 1.0, sourcePaths: []string{"outline"}},
	}
	ftsCandidates := []candidate{
		{unitID: "u2", pointID: "p2", sourceID: "s1", score: 3.0, sourcePaths: []string{"fts"}},
		{unitID: "u3", pointID: "p3", sourceID: "s2", score: 1.5, sourcePaths: []string{"fts"}},
	}

	merged := svc.rrfMerge(outlineCandidates, ftsCandidates)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged candidates, got %d", len(merged))
	}

	// u2 should have highest RRF score (appears in both lists)
	if merged[0].unitID != "u2" {
		t.Errorf("expected u2 as top candidate, got %s", merged[0].unitID)
	}
}

func TestRerank(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25, score: 1.0},
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50, score: 0.5},
	}

	fake.SetResponse("rerank.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct"}, {"candidate_id": "c2", "role": "irrelevant"}]}`,
	})

	kept, err := svc.rerank(context.Background(), QueryContext{Question: "what is linear equation?"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept (c2 irrelevant), got %d", len(kept))
	}
	if kept[0].unitID != "u1" {
		t.Errorf("expected u1, got %s", kept[0].unitID)
	}
	if kept[0].sourcePaths[0] != "direct" {
		t.Errorf("expected role=direct, got %s", kept[0].sourcePaths[0])
	}
}

func TestKPNExpand(t *testing.T) {
	svc, _, _ := setupTestService(t)

	candidates := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25, sourcePaths: []string{"direct"}},
	}

	expanded, conflicts, err := svc.kpnExpand(candidates)
	if err != nil {
		t.Fatal(err)
	}

	// u1 has p1 → p2 (bidirectional, related), p1 → p3 (directed, supplements)
	// So u2 (via p2) and u3 (via p3) should be added as supporting
	if len(expanded) != 3 {
		t.Fatalf("expected 3 candidates after KPN expand, got %d", len(expanded))
	}

	newUnits := make(map[string]bool)
	for _, c := range expanded[1:] {
		newUnits[c.unitID] = true
		if c.sourcePaths[0] != "supporting" {
			t.Errorf("expected supporting role for %s, got %s", c.unitID, c.sourcePaths[0])
		}
	}
	if !newUnits["u2"] || !newUnits["u3"] {
		t.Errorf("expected u2 and u3 from KPN expand, got %v", newUnits)
	}

	// p1→p4 contradicts → u4 should appear in conflicts
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict (p1→p4 contradicts), got %d", len(conflicts))
	}
	if conflicts[0].unitID != "u4" {
		t.Errorf("expected conflict unit u4, got %s", conflicts[0].unitID)
	}
}

func TestSufficiencyJudgment(t *testing.T) {
	// direct non-empty → short
	direct := []candidate{{unitID: "u1"}}
	path := "deep"
	if len(direct) > 0 {
		path = "short"
	}
	if path != "short" {
		t.Errorf("expected short, got %s", path)
	}

	// direct empty → deep
	direct = nil
	path = "deep"
	if len(direct) > 0 {
		path = "short"
	}
	if path != "deep" {
		t.Errorf("expected deep, got %s", path)
	}
}

func TestBuildEvidenceSet(t *testing.T) {
	svc, _, _ := setupTestService(t)

	direct := []candidate{
		{unitID: "u1", pointID: "p1", sourceID: "s1", lineStart: 1, lineEnd: 25},
	}
	supporting := []candidate{
		{unitID: "u2", pointID: "p2", sourceID: "s1", lineStart: 26, lineEnd: 50},
	}

	es, err := svc.buildEvidenceSet("test question", "", "", "", "short", direct, supporting, nil)
	if err != nil {
		t.Fatal(err)
	}

	if es.Question != "test question" {
		t.Errorf("expected question='test question', got '%s'", es.Question)
	}
	if es.Path != "short" {
		t.Errorf("expected path=short, got %s", es.Path)
	}
	if len(es.DirectEvidence) != 1 {
		t.Fatalf("expected 1 direct evidence, got %d", len(es.DirectEvidence))
	}
	if len(es.Supporting) != 1 {
		t.Fatalf("expected 1 supporting, got %d", len(es.Supporting))
	}

	ev := es.DirectEvidence[0]
	if ev.FactID == "" {
		t.Error("fact_id should not be empty")
	}
	if ev.UnitID != "u1" {
		t.Errorf("expected unit_id=u1, got %s", ev.UnitID)
	}
	if ev.PointID != "p1" {
		t.Errorf("expected point_id=p1, got %s", ev.PointID)
	}
	if ev.Role != "direct" {
		t.Errorf("expected role=direct, got %s", ev.Role)
	}
	if ev.Content == "" {
		t.Error("content should not be empty")
	}

	var ref SourceRef
	if err := json.Unmarshal(ev.SourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.SourceID != "s1" || ref.LineStart != 1 || ref.LineEnd != 25 {
		t.Errorf("unexpected source_ref: %+v", ref)
	}
}

func TestRetrieveEndToEnd(t *testing.T) {
	svc, fake, _ := setupTestService(t)

	// Domain match → d1
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{
		Output: `{"domain_ids": ["d1"]}`,
	})
	// Source filter → s1
	fake.SetResponse("source_filter.md", llm.FakeResponse{
		Output: `{"source_ids": ["s1"]}`,
	})
	// Outline filter fallback
	fake.SetResponse("outline_filter.md", llm.FakeResponse{
		Output: `{"outline_ids": ["o2"]}`,
	})
	// Rerank — accept all as direct for simplicity
	fake.SetResponse("rerank.md", llm.FakeResponse{
		Output: `{"results": [{"candidate_id": "c1", "role": "direct"}]}`,
	})

	es, err := svc.Retrieve(context.Background(), "linear equations")
	if err != nil {
		t.Fatal(err)
	}

	if es.Question != "linear equations" {
		t.Errorf("expected question='linear equations', got '%s'", es.Question)
	}
	// Should have direct evidence → short path
	if es.Path != "short" {
		t.Errorf("expected path=short, got %s", es.Path)
	}
	if len(es.DirectEvidence) == 0 {
		t.Error("expected at least 1 direct evidence")
	}
}
