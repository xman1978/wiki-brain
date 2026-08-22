package unit

import (
	"database/sql"
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

// TestPublishGenerationWritesEachPointsOwnSemantics covers the KP-level
// semantics design (docs/impl/v1/semantics-curation.md 2026-08-21 改判):
// each candidate point carries its own source_theme/content_theme/object/
// scope, written straight to its knowledge_points row — no separate
// validation gate, no whole-unit discard when a point's semantics are
// incomplete (object may legitimately be blank; a point with no theme/scope
// at all is still published, just with an empty semantics_prompt_version,
// to be picked up later by RegenerateRerankSemantics).
func TestPublishGenerationWritesEachPointsOwnSemantics(t *testing.T) {
	store := setupTestStore(t)
	pool := []unitCandidate{
		{
			id: "u1", llm: llmUnit{Center: "Policy limits"},
			lineStart: 1, lineEnd: 1, promptVersion: promptVersionSplitExtract,
			points: []llmPoint{
				{Content: "employees only", Type: "rule", SourceTheme: "policy", ContentTheme: "limits", Object: "employees", Scope: "travel", SemanticsPromptVersion: rerank.ExtractPromptVersion},
				{Content: "no stated object", Type: "rule", SourceTheme: "policy", ContentTheme: "limits", Object: "", Scope: "travel", SemanticsPromptVersion: rerank.ExtractPromptVersion},
				{Content: "never got semantics", Type: "rule"},
			},
		},
	}

	_, inserted, points, err := store.PublishGeneration("src-1", pool)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if len(inserted) != 1 || inserted[0].UnitID != "u1" {
		t.Fatalf("inserted = %+v, want only u1", inserted)
	}
	if len(points) != 3 {
		t.Fatalf("points = %+v, want 3", points)
	}
	byContent := make(map[string]KnowledgePoint, len(points))
	for _, p := range points {
		byContent[p.Content] = p
	}
	if p := byContent["employees only"]; p.Object != "employees" || p.Scope != "travel" || p.SemanticsPromptVersion != rerank.ExtractPromptVersion {
		t.Fatalf("point with full semantics = %+v", p)
	}
	if p := byContent["no stated object"]; p.Object != "" || p.Scope != "travel" || p.SemanticsPromptVersion != rerank.ExtractPromptVersion {
		t.Fatalf("point with blank object = %+v, want object blank but still stamped current", p)
	}
	if p := byContent["never got semantics"]; p.SemanticsPromptVersion != "" {
		t.Fatalf("point with no semantics = %+v, want empty semantics_prompt_version (not discarded)", p)
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
	if got.EntryID.Valid {
		t.Error("entry_id should be null initially")
	}

	store.db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('d-1', 'TestDomain')`)
	store.db.Exec(`INSERT INTO entries (entry_id, domain_id, name) VALUES ('c-1', 'd-1', 'TestEntry')`)

	cid := "c-1"
	if err := store.UpdateUnitEntryID(ku.UnitID, &cid); err != nil {
		t.Fatalf("update entry_id: %v", err)
	}

	got, _ = store.GetUnitByID(ku.UnitID)
	if !got.EntryID.Valid || got.EntryID.String != "c-1" {
		t.Errorf("entry_id = %v, want c-1", got.EntryID)
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

// TestGetEntriesByDomainID_ExcludesMerged covers docs/impl/v1/concept-evolution.md
// 步骤 4: a merged concept must not appear in unit_entry_match's candidate list.
func TestGetEntriesByDomainID_ExcludesMerged(t *testing.T) {
	store := setupTestStore(t)

	if _, err := store.db.Exec(`INSERT INTO domains (domain_id, name) VALUES ('d1', 'Domain One')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO entries (entry_id, domain_id, name) VALUES ('c-active', 'd1', 'Active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO entries (entry_id, domain_id, name, merged_into) VALUES ('c-merged', 'd1', 'Merged', 'c-active')`); err != nil {
		t.Fatal(err)
	}

	entries, err := store.GetEntriesByDomainID("d1")
	if err != nil {
		t.Fatalf("get entries: %v", err)
	}
	if len(entries) != 1 || entries[0].EntryID != "c-active" {
		t.Errorf("expected only c-active, got %+v", entries)
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
