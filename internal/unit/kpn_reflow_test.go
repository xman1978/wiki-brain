package unit

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// seedWikiPage inserts a minimal wiki_pages row citing pointIDs — enough for
// AncestorPointIDsForWikiPage to resolve the self-ancestor set
// (docs/impl/v1/wiki.md 步骤 10 "回流的自体循环必须挡住").
func seedWikiPage(t *testing.T, db *sql.DB, pageID string, pointIDs []string) {
	t.Helper()
	pointIDsJSON, err := json.Marshal(pointIDs)
	if err != nil {
		t.Fatalf("marshal point ids: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO wiki_pages
		(page_id, page_type, title, content, status, source_point_ids, prompt_version, model_name)
		VALUES (?, 'concept', 'Ancestor Page', 'content', 'published', ?, 'v1', 'test')`,
		pageID, string(pointIDsJSON)); err != nil {
		t.Fatalf("seed wiki page: %v", err)
	}
}

func markSourceAsWikiDraft(t *testing.T, db *sql.DB, sourceID, originPageID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE sources SET origin = 'wiki_draft', origin_page_id = ? WHERE source_id = ?`,
		originPageID, sourceID); err != nil {
		t.Fatalf("mark source as wiki_draft: %v", err)
	}
}

// TestCrossSourceKPN_SkipsSelfAncestorEdges implements docs/impl/v1/wiki.md
// 完成标准: "把某主题页的草稿导出后以 origin='wiki_draft' 导入，新 KP 与
// origin_page_id 页面已引用的 KP 之间不产生 KPN 关系...同一批回流 KP 与其他
// 知识之间的关系照常建立".
func TestCrossSourceKPN_SkipsSelfAncestorEdges(t *testing.T) {
	svc, fake, db := setupTestService(t)
	store := NewStore(db)

	seedDomain(t, db, "d1", "D")
	seedConcept(t, db, "c1", "d1", "C")
	seedSourceWithDomain(t, db, "reflow-src", "d1")
	seedSourceWithDomain(t, db, "ancestor-src", "d1")
	seedSourceWithDomain(t, db, "other-src", "d1")

	// reflow-src's new KP.
	seedKUWithConcept(t, store, "ku-reflow", "reflow-src", "c1", "reflow topic")
	seedKP(t, store, "kp-reflow", "ku-reflow", "reflow-src", "reflow content")

	// ancestor-src's KP is what the origin page already cited — must be
	// excluded from cross matching for this reflow source.
	seedKUWithConcept(t, store, "ku-ancestor", "ancestor-src", "c1", "ancestor topic")
	seedKP(t, store, "kp-ancestor", "ku-ancestor", "ancestor-src", "ancestor content")

	// other-src's KP is unrelated knowledge — matching against it must
	// proceed normally (the exclusion only targets the self-ancestor edge).
	seedKUWithConcept(t, store, "ku-other", "other-src", "c1", "other topic")
	seedKP(t, store, "kp-other", "ku-other", "other-src", "other content")

	seedWikiPage(t, db, "page-ancestor", []string{"kp-ancestor"})
	markSourceAsWikiDraft(t, db, "reflow-src", "page-ancestor")

	fake.SetResponse("kpn_cross_match.md", llm.FakeResponse{
		Output: `{"relations": [
			{"from": "kp-reflow", "to": "kp-ancestor", "type": "related"},
			{"from": "kp-reflow", "to": "kp-other", "type": "related"}
		]}`,
	})

	result, err := svc.CrossSourceKPN(context.Background(), "reflow-src")
	if err != nil {
		t.Fatalf("CrossSourceKPN: %v", err)
	}
	if result.RelationsCreated != 1 {
		t.Fatalf("expected exactly 1 relation created (kp-reflow<->kp-other only), got %d", result.RelationsCreated)
	}

	rels, err := store.GetRelationsByPointID("kp-reflow", "")
	if err != nil {
		t.Fatalf("get relations: %v", err)
	}
	for _, r := range rels {
		if r.TargetPointID == "kp-ancestor" || r.SourcePointID == "kp-ancestor" {
			t.Errorf("self-ancestor edge to kp-ancestor must not be created, got relation %+v", r)
		}
	}
	foundOther := false
	for _, r := range rels {
		if r.TargetPointID == "kp-other" || r.SourcePointID == "kp-other" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Error("expected relation to kp-other (unrelated knowledge) to be created normally")
	}

	src, err := svc.sourceStore.GetByID("reflow-src")
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.ReflowSkippedEdges != 1 {
		t.Errorf("expected reflow_skipped_edges=1, got %d", src.ReflowSkippedEdges)
	}
}
