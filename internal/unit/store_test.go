package unit

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/rerank"
)

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	db := foundation.NewTestDB(t)
	insertTestSource(t, db)
	return NewStore(db)
}

func insertTestSource(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('src-1', 'Test Source', 'markdown', 'test.md', '/tmp/test.md', '/tmp/test.md', 'completed')`)
	if err != nil {
		t.Fatalf("insert test source: %v", err)
	}
	_, err = db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-1', 'src-1', 1, 'Chapter 1', 1, 50, 'structural', 1)`)
	if err != nil {
		t.Fatalf("insert test outline: %v", err)
	}
}

func TestPublishGenerationRejectsInvalidSemanticsBeforeWriting(t *testing.T) {
	valid := rerank.Semantics{
		UnitID:        "u1",
		SourceTheme:   "policy",
		ContentTheme:  "limits",
		Intent:        "explain",
		Object:        "employees",
		Scope:         "travel",
		PromptVersion: rerank.ExtractPromptVersion,
	}
	tests := []struct {
		name   string
		mutate func(*rerank.Semantics)
		want   string
	}{
		{name: "empty unit id", mutate: func(s *rerank.Semantics) { s.UnitID = "" }, want: "unit_id"},
		{name: "wrong prompt version", mutate: func(s *rerank.Semantics) { s.PromptVersion = "v0" }, want: "prompt_version"},
		{name: "empty source theme", mutate: func(s *rerank.Semantics) { s.SourceTheme = " " }, want: "source_theme"},
		{name: "empty content theme", mutate: func(s *rerank.Semantics) { s.ContentTheme = "" }, want: "content_theme"},
		{name: "empty intent", mutate: func(s *rerank.Semantics) { s.Intent = "" }, want: "intent"},
		{name: "empty object", mutate: func(s *rerank.Semantics) { s.Object = "" }, want: "object"},
		{name: "empty scope", mutate: func(s *rerank.Semantics) { s.Scope = "" }, want: "scope"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			semantic := valid
			tc.mutate(&semantic)
			pool := []unitCandidate{{
				id: "u1", llm: llmUnit{Center: "Policy limits"},
				lineStart: 1, lineEnd: 1, promptVersion: promptVersionSplitExtract,
			}}

			_, _, _, err := store.PublishGeneration("src-1", pool, map[string]rerank.Semantics{"u1": semantic})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PublishGeneration err = %v, want field %q", err, tc.want)
			}
			var units, semantics int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_units WHERE source_id = 'src-1'`).Scan(&units); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM unit_rerank_semantics`).Scan(&semantics); err != nil {
				t.Fatal(err)
			}
			if units != 0 || semantics != 0 {
				t.Fatalf("rows after rejected publication: units=%d semantics=%d, want 0/0", units, semantics)
			}
		})
	}
}

// TestPublishGenerationDiscardsUnitWhenSemanticsMissing covers the "discard
// units semantics extraction gave up on" policy: a candidate simply absent
// from the semantics map (as opposed to present-but-invalid, covered by
// TestPublishGenerationRejectsInvalidSemanticsBeforeWriting) must not be
// published at all — no knowledge_units row, no knowledge_points rows —
// while every other candidate in the same generation still publishes
// normally.
func TestPublishGenerationDiscardsUnitWhenSemanticsMissing(t *testing.T) {
	store := setupTestStore(t)
	pool := []unitCandidate{
		{id: "u1", llm: llmUnit{Center: "Policy limits"}, lineStart: 1, lineEnd: 1, promptVersion: promptVersionSplitExtract},
		{id: "u2", llm: llmUnit{Center: "Heading only"}, lineStart: 2, lineEnd: 2, promptVersion: promptVersionSplitExtract},
	}
	semantics := map[string]rerank.Semantics{
		"u1": {
			UnitID: "u1", SourceTheme: "policy", ContentTheme: "limits", Intent: "explain",
			Object: "employees", Scope: "travel",
			PromptVersion: rerank.ExtractPromptVersion,
		},
		// u2 intentionally has no entry — simulates extractRerankSemantics
		// giving up on it after every fallback tier.
	}

	_, inserted, _, err := store.PublishGeneration("src-1", pool, semantics)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if len(inserted) != 1 || inserted[0].UnitID != "u1" {
		t.Fatalf("inserted = %+v, want only u1 (u2 discarded, semantics never resolved)", inserted)
	}

	var unitCount, semanticsCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_units WHERE source_id = 'src-1'`).Scan(&unitCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM unit_rerank_semantics`).Scan(&semanticsCount); err != nil {
		t.Fatal(err)
	}
	if unitCount != 1 {
		t.Fatalf("knowledge_units rows = %d, want 1 (u2 discarded entirely)", unitCount)
	}
	if semanticsCount != 1 {
		t.Fatalf("unit_rerank_semantics rows = %d, want 1 (only u1)", semanticsCount)
	}
	var semanticUnitID string
	if err := store.db.QueryRow(`SELECT unit_id FROM unit_rerank_semantics`).Scan(&semanticUnitID); err != nil {
		t.Fatal(err)
	}
	if semanticUnitID != "u1" {
		t.Fatalf("unit_rerank_semantics row is for %q, want u1", semanticUnitID)
	}
}

func TestInsertAndGetUnit(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{
		SourceID:      "src-1",
		OutlineID:     sql.NullString{String: "ol-1", Valid: true},
		Center:        "测试知识单元主题",
		LineStart:     1,
		LineEnd:       10,
		Status:        "completed",
		PromptVersion: "v1",
	}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}
	if ku.UnitID == "" {
		t.Fatal("expected unit_id to be generated")
	}

	got, err := store.GetUnitByID(ku.UnitID)
	if err != nil {
		t.Fatalf("get unit: %v", err)
	}
	if got.Center != "测试知识单元主题" {
		t.Errorf("center = %q, want %q", got.Center, "测试知识单元主题")
	}
	if got.LineStart != 1 || got.LineEnd != 10 {
		t.Errorf("line range = %d-%d, want 1-10", got.LineStart, got.LineEnd)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
}

func TestInsertAndGetPoints(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{
		SourceID:      "src-1",
		Center:        "主题",
		LineStart:     1,
		LineEnd:       5,
		Status:        "completed",
		PromptVersion: "v1",
	}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	kp := &KnowledgePoint{
		UnitID:    ku.UnitID,
		SourceID:  "src-1",
		Content:   "知识点内容",
		PointType: "definition",
	}
	if err := store.InsertPoint(kp); err != nil {
		t.Fatalf("insert point: %v", err)
	}
	if kp.PointID == "" {
		t.Fatal("expected point_id to be generated")
	}

	points, err := store.GetPointsByUnitID(ku.UnitID)
	if err != nil {
		t.Fatalf("get points: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}
	if points[0].Content != "知识点内容" {
		t.Errorf("content = %q, want %q", points[0].Content, "知识点内容")
	}
	if points[0].PointType != "definition" {
		t.Errorf("point_type = %q, want definition", points[0].PointType)
	}
}

func TestGetPointByID(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{SourceID: "src-1", Center: "主题", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v3"}
	store.InsertUnit(ku)

	kp := &KnowledgePoint{UnitID: ku.UnitID, SourceID: "src-1", Content: "测试知识点", PointType: "definition"}
	store.InsertPoint(kp)

	got, err := store.GetPointByID(kp.PointID)
	if err != nil {
		t.Fatalf("get point by id: %v", err)
	}
	if got.UnitID != ku.UnitID {
		t.Errorf("unit_id = %q, want %q", got.UnitID, ku.UnitID)
	}
	if got.Content != "测试知识点" {
		t.Errorf("content = %q, want 测试知识点", got.Content)
	}

	_, err = store.GetPointByID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent point_id")
	}
}

func TestInsertAndGetRelations(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{SourceID: "src-1", Center: "主题", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	ku2 := &KnowledgeUnit{SourceID: "src-1", Center: "主题2", LineStart: 6, LineEnd: 10, Status: "completed", PromptVersion: "v1"}
	if err := store.InsertUnit(ku2); err != nil {
		t.Fatalf("insert unit2: %v", err)
	}

	kp1 := &KnowledgePoint{UnitID: ku.UnitID, SourceID: "src-1", Content: "点1", PointType: "definition"}
	kp2 := &KnowledgePoint{UnitID: ku2.UnitID, SourceID: "src-1", Content: "点2", PointType: "rule"}
	if err := store.InsertPoint(kp1); err != nil {
		t.Fatalf("insert point1: %v", err)
	}
	if err := store.InsertPoint(kp2); err != nil {
		t.Fatalf("insert point2: %v", err)
	}

	rel := &KnowledgePointRelation{
		SourcePointID: kp1.PointID,
		TargetPointID: kp2.PointID,
		RelationType:  "related",
		Direction:     "bidirectional",
		PromptVersion: "v1",
	}
	if _, err := store.InsertRelation(rel); err != nil {
		t.Fatalf("insert relation: %v", err)
	}

	rels, err := store.GetRelationsByPointID(kp1.PointID, "")
	if err != nil {
		t.Fatalf("get relations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relations, want 1", len(rels))
	}
	if rels[0].RelationType != "related" {
		t.Errorf("relation_type = %q, want related", rels[0].RelationType)
	}
	if rels[0].Direction != "bidirectional" {
		t.Errorf("direction = %q, want bidirectional", rels[0].Direction)
	}

	rels2, err := store.GetRelationsByPointID(kp2.PointID, "")
	if err != nil {
		t.Fatalf("get relations for point2: %v", err)
	}
	if len(rels2) != 1 {
		t.Fatal("bidirectional relation should be found from either side")
	}
}

func TestUpdateUnitStatus(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{SourceID: "src-1", Center: "主题", LineStart: 1, LineEnd: 5, Status: "pending", PromptVersion: "v1"}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	errMsg := "extraction failed"
	if err := store.UpdateUnitStatus(ku.UnitID, "extraction_failed", &errMsg); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := store.GetUnitByID(ku.UnitID)
	if got.Status != "extraction_failed" {
		t.Errorf("status = %q, want extraction_failed", got.Status)
	}
	if !got.ErrorMsg.Valid || got.ErrorMsg.String != "extraction failed" {
		t.Errorf("error_msg = %v, want 'extraction failed'", got.ErrorMsg)
	}
}

func TestUpdateUnitConceptID(t *testing.T) {
	store := setupTestStore(t)

	ku := &KnowledgeUnit{SourceID: "src-1", Center: "主题", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	if err := store.InsertUnit(ku); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	got, _ := store.GetUnitByID(ku.UnitID)
	if got.ConceptID.Valid {
		t.Error("concept_id should be null initially")
	}

	store.db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('d-1', 'TestDomain')`)
	store.db.Exec(`INSERT INTO concepts (concept_id, domain_id, name) VALUES ('c-1', 'd-1', 'TestConcept')`)

	cid := "c-1"
	if err := store.UpdateUnitConceptID(ku.UnitID, &cid); err != nil {
		t.Fatalf("update concept_id: %v", err)
	}

	got, _ = store.GetUnitByID(ku.UnitID)
	if !got.ConceptID.Valid || got.ConceptID.String != "c-1" {
		t.Errorf("concept_id = %v, want c-1", got.ConceptID)
	}
}

func TestGetUnitsBySourceID(t *testing.T) {
	store := setupTestStore(t)

	for i := 0; i < 3; i++ {
		ku := &KnowledgeUnit{
			SourceID: "src-1", Center: "主题", LineStart: i*10 + 1, LineEnd: (i + 1) * 10,
			Status: "completed", PromptVersion: "v1",
		}
		store.InsertUnit(ku)
	}

	units, err := store.GetUnitsBySourceID("src-1")
	if err != nil {
		t.Fatalf("get units: %v", err)
	}
	if len(units) != 3 {
		t.Errorf("got %d units, want 3", len(units))
	}
}

// TestGetConceptsByDomainID_ExcludesMerged covers docs/impl/v1/concept-evolution.md
// 步骤 4: a merged concept must not appear in unit_concept_match's candidate list.
func TestGetConceptsByDomainID_ExcludesMerged(t *testing.T) {
	store := setupTestStore(t)

	if _, err := store.db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('d1', 'Domain One')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO concepts (concept_id, domain_id, name) VALUES ('c-active', 'd1', 'Active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO concepts (concept_id, domain_id, name, merged_into) VALUES ('c-merged', 'd1', 'Merged', 'c-active')`); err != nil {
		t.Fatal(err)
	}

	concepts, err := store.GetConceptsByDomainID("d1")
	if err != nil {
		t.Fatalf("get concepts: %v", err)
	}
	if len(concepts) != 1 || concepts[0].ConceptID != "c-active" {
		t.Errorf("expected only c-active, got %+v", concepts)
	}
}

func TestGetPointsBySourceID(t *testing.T) {
	store := setupTestStore(t)

	kuCompleted := &KnowledgeUnit{SourceID: "src-1", Center: "完成", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"}
	kuFailed := &KnowledgeUnit{SourceID: "src-1", Center: "失败", LineStart: 6, LineEnd: 10, Status: "extraction_failed", PromptVersion: "v1"}
	store.InsertUnit(kuCompleted)
	store.InsertUnit(kuFailed)

	store.InsertPoint(&KnowledgePoint{UnitID: kuCompleted.UnitID, SourceID: "src-1", Content: "好的", PointType: "definition"})
	store.InsertPoint(&KnowledgePoint{UnitID: kuFailed.UnitID, SourceID: "src-1", Content: "坏的", PointType: "definition"})

	points, err := store.GetPointsBySourceID("src-1")
	if err != nil {
		t.Fatalf("get points: %v", err)
	}
	if len(points) != 1 {
		t.Errorf("got %d points, want 1 (only from completed units)", len(points))
	}
}
