package unit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
)

// setupCurationTest builds a service backed by a real markdown file so the
// semantics view can slice unit content by line range.
func setupCurationTest(t *testing.T) (*Service, string) {
	t.Helper()
	db := foundation.NewTestDB(t)

	mdPath := filepath.Join(t.TempDir(), "doc.md")
	content := "第一行\n第二行：培训积分不跨年累计，次年自动清零。\n第三行\n第四行\n第五行"
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES ('src-1', '培训积分管理办法', 'markdown', 'doc.md', ?, ?, 'completed')`, mdPath, mdPath); err != nil {
		t.Fatal(err)
	}

	// 两级目录链（parent → leaf）：验证 outline_path 按 root→leaf 拼接。
	if _, err := db.Exec(`INSERT INTO source_outlines (outline_id, source_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-root', 'src-1', 1, '第三章 积分结果公布及应用', 1, 5, 'structural', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_outlines (outline_id, source_id, parent_id, level, title, line_start, line_end, node_type, position)
		VALUES ('ol-leaf', 'src-1', 'ol-root', 2, '第六条 积分统计及公布', 2, 4, 'semantic', 2)`); err != nil {
		t.Fatal(err)
	}

	unitStore := NewStore(db)
	if err := unitStore.InsertUnit(&KnowledgeUnit{
		UnitID: "ku-1", SourceID: "src-1",
		OutlineID: sql.NullString{String: "ol-leaf", Valid: true},
		Center:    "培训积分的统计、清零与公布规则", LineStart: 2, LineEnd: 4,
		Status: "completed", PromptVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}

	svc := &Service{store: unitStore, sourceStore: source.NewStore(db)}
	return svc, mdPath
}

func curationMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	(&Handler{svc: svc}).RegisterRoutes(mux)
	return mux
}

func TestGetPointSemantics_MissingSemantics(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "培训积分不跨年累计，次年自动清零", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/points/kp-1/semantics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Content         string `json:"content"`
		ContentTheme    string `json:"content_theme"`
		ManuallyEdited  bool   `json:"manually_edited"`
		OutlinePath     string `json:"outline_path"`
		OutlineNodeType string `json:"outline_node_type"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ContentTheme != "" {
		t.Errorf("content_theme = %q, want empty for a point with no semantics yet", resp.ContentTheme)
	}
	if resp.Content != "培训积分不跨年累计，次年自动清零" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.OutlinePath != "第三章 积分结果公布及应用 / 第六条 积分统计及公布" {
		t.Errorf("outline_path = %q, want root→leaf chain", resp.OutlinePath)
	}
	if resp.OutlineNodeType != "semantic" {
		t.Errorf("outline_node_type = %q, want leaf's node_type \"semantic\"", resp.OutlineNodeType)
	}
}

func TestPutPointSemantics_CreatesAndUpdates(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "培训积分不跨年累计，次年自动清零", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	body := `{"source_theme":"培训积分管理办法","content_theme":"积分统计与公布机制","object":"培训积分","scope":"全体员工"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/points/kp-1/semantics", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	kp, err := svc.store.GetPointByID("kp-1")
	if err != nil {
		t.Fatal(err)
	}
	if !kp.ManuallyEdited || !kp.EditedAt.Valid {
		t.Errorf("manually_edited/edited_at = %v/%v, want true/set", kp.ManuallyEdited, kp.EditedAt.Valid)
	}
	if kp.SemanticsPromptVersion != rerank.ExtractPromptVersion {
		t.Errorf("prompt_version = %q, want %q on create", kp.SemanticsPromptVersion, rerank.ExtractPromptVersion)
	}
	if kp.ContentTheme != "积分统计与公布机制" {
		t.Errorf("content_theme = %q", kp.ContentTheme)
	}

	body2 := `{"source_theme":"培训积分管理办法","content_theme":"改","object":"培训积分","scope":"全体员工"}`
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("PUT", "/points/kp-1/semantics", bytes.NewBufferString(body2)))
	if w2.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d; body=%s", w2.Code, w2.Body.String())
	}
	kp, err = svc.store.GetPointByID("kp-1")
	if err != nil {
		t.Fatal(err)
	}
	if kp.ContentTheme != "改" {
		t.Errorf("content_theme = %q, want updated value", kp.ContentTheme)
	}
}

func TestPutPointSemantics_Validation(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "x", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	for name, body := range map[string]string{
		"missing field":  `{"source_theme":"a","content_theme":"b","object":"d","scope":""}`,
		"malformed json": `{`,
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("PUT", "/points/kp-1/semantics", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestPutPointSemantics_NonCurrentUnitRejected(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "x", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'ku-1'`); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	body := `{"source_theme":"a","content_theme":"b","object":"d","scope":"e"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/points/kp-1/semantics", bytes.NewBufferString(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for superseded unit", w.Code)
	}
}

func TestGetPointSemantics_NotFound(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/points/nope/semantics", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// kpnKnownRelationClient fakes kpn_extract.md by reading back whatever
// point_id the incremental KPN batch actually assigned the new manual KP
// (a fresh uuid, unknown to the test in advance) out of the
// "point_id\tcenter\tcontent" lines the prompt is built from, and proposing a
// related relation between it and a known existing point.
type kpnKnownRelationClient struct {
	*llm.FakeClient
	existingPointID string
}

func (f *kpnKnownRelationClient) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	if promptFile != "kpn_extract.md" {
		return f.FakeClient.CompleteJSON(ctx, promptFile, vars, model)
	}
	var newID string
	for _, line := range strings.Split(strings.TrimRight(vars["knowledge_points"], "\n"), "\n") {
		id := strings.SplitN(line, "\t", 2)[0]
		if id != "" && id != f.existingPointID {
			newID = id
		}
	}
	return json.Marshal(map[string]any{
		"relations": []map[string]string{{"from": f.existingPointID, "to": newID, "type": "related"}},
	})
}

func TestAddPoint_CreatesManualPointAndTriggersIncrementalKPN(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-existing", UnitID: "ku-1", SourceID: "src-1",
		Content: "existing fact", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	svc.llmClient = &kpnKnownRelationClient{FakeClient: llm.NewFakeClient(), existingPointID: "kp-existing"}
	mux := curationMux(svc)

	body := `{"content":"培训积分不跨年累计，次年自动清零","point_type":"rule"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/units/ku-1/points", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		PointID          string `json:"point_id"`
		ManuallyEdited   bool   `json:"manually_edited"`
		RelationsCreated int    `json:"relations_created"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.PointID == "" || !resp.ManuallyEdited {
		t.Fatalf("resp = %+v, want a point_id and manually_edited=true", resp)
	}
	if resp.RelationsCreated != 1 {
		t.Fatalf("relations_created = %d, want 1 (kp-existing <-> new point)", resp.RelationsCreated)
	}

	kp, err := svc.store.GetPointByID(resp.PointID)
	if err != nil {
		t.Fatal(err)
	}
	if kp.Content != "培训积分不跨年累计，次年自动清零" || kp.PointType != "rule" || !kp.ManuallyEdited {
		t.Fatalf("stored point = %+v", kp)
	}

	rels, err := svc.store.GetRelationsByPointID("kp-existing", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].RelationType != "related" {
		t.Fatalf("relations = %+v, want one related relation", rels)
	}
}

func TestAddPoint_Validation(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	for name, body := range map[string]string{
		"empty content": `{"content":"","point_type":"rule"}`,
		"invalid type":  `{"content":"x","point_type":"opinion"}`,
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/units/ku-1/points", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestAddPoint_NonCurrentUnitRejected(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if _, err := svc.store.db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'ku-1'`); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	body := `{"content":"x","point_type":"rule"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/units/ku-1/points", bytes.NewBufferString(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for superseded unit", w.Code)
	}
}

func TestUpdatePoint_EditsContentAndMarksManual(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "original fact", PointType: "definition",
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	body := `{"content":"corrected fact","point_type":"rule"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/points/kp-1", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	kp, err := svc.store.GetPointByID("kp-1")
	if err != nil {
		t.Fatal(err)
	}
	if kp.Content != "corrected fact" || kp.PointType != "rule" || !kp.ManuallyEdited || !kp.EditedAt.Valid {
		t.Fatalf("stored point = %+v", kp)
	}
}

func TestUpdatePoint_NotFound(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	body := `{"content":"x","point_type":"rule"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/points/nope", bytes.NewBufferString(body)))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeprecatePoint_ManualSucceeds(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertManualPoint(&KnowledgePoint{
		PointID: "kp-manual", UnitID: "ku-1", SourceID: "src-1",
		Content: "wrong fact", PointType: "rule", Lifecycle: LifecycleCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/points/kp-manual/deprecate", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("POST deprecate status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		PointID   string `json:"point_id"`
		Lifecycle string `json:"lifecycle"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.PointID != "kp-manual" || resp.Lifecycle != LifecycleDeprecated {
		t.Fatalf("resp = %+v", resp)
	}

	kp, err := svc.store.GetPointByID("kp-manual")
	if err != nil {
		t.Fatal(err)
	}
	if kp.Lifecycle != LifecycleDeprecated {
		t.Fatalf("lifecycle = %q, want deprecated", kp.Lifecycle)
	}

	// Idempotent: second call still 200.
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("POST", "/points/kp-manual/deprecate", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second deprecate status = %d, want 200", w2.Code)
	}
}

func TestDeprecatePoint_ExtractedRejected(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if err := svc.store.InsertPoint(&KnowledgePoint{
		PointID: "kp-auto", UnitID: "ku-1", SourceID: "src-1",
		Content: "extracted fact", PointType: "rule",
	}); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/points/kp-auto/deprecate", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for extracted KP; body=%s", w.Code, w.Body.String())
	}
	kp, err := svc.store.GetPointByID("kp-auto")
	if err != nil {
		t.Fatal(err)
	}
	if kp.Lifecycle != LifecycleCurrent {
		t.Fatalf("extracted KP lifecycle changed to %q", kp.Lifecycle)
	}
}

func TestDeprecatePoint_NotFound(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/points/nope/deprecate", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
