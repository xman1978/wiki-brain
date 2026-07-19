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

func TestGetUnitSemantics_MissingRow(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/units/ku-1/semantics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Unit struct {
			Center          string `json:"center"`
			LineStart       int    `json:"line_start"`
			LineEnd         int    `json:"line_end"`
			Lifecycle       string `json:"lifecycle"`
			Content         string `json:"content"`
			OutlinePath     string `json:"outline_path"`
			OutlineNodeType string `json:"outline_node_type"`
		} `json:"unit"`
		Semantics *json.RawMessage `json:"semantics"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Semantics != nil && string(*resp.Semantics) != "null" {
		t.Errorf("semantics = %s, want null for a unit with no semantics row", string(*resp.Semantics))
	}
	if resp.Unit.Center != "培训积分的统计、清零与公布规则" {
		t.Errorf("center = %q", resp.Unit.Center)
	}
	want := "第二行：培训积分不跨年累计，次年自动清零。\n第三行\n第四行"
	if resp.Unit.Content != want {
		t.Errorf("content = %q, want L2-L4 slice %q", resp.Unit.Content, want)
	}
	if resp.Unit.OutlinePath != "第三章 积分结果公布及应用 / 第六条 积分统计及公布" {
		t.Errorf("outline_path = %q, want root→leaf chain", resp.Unit.OutlinePath)
	}
	if resp.Unit.OutlineNodeType != "semantic" {
		t.Errorf("outline_node_type = %q, want leaf's node_type \"semantic\"", resp.Unit.OutlineNodeType)
	}
}

func TestPutUnitSemantics_CreatesAndUpdates(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	body := `{"semantics":{"source_theme":"培训积分管理办法","content_theme":"积分统计与公布机制","intent":"规则","object":"培训积分","scope":"全体员工"}}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/units/ku-1/semantics", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// The created row is manually_edited, carries the current extract prompt
	// version (it was missing before), and round-trips through GET.
	row, err := svc.store.GetRerankSemanticsByUnitID("ku-1")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("semantics row not created")
	}
	if !row.ManuallyEdited || !row.EditedAt.Valid {
		t.Errorf("manually_edited/edited_at = %v/%v, want true/set", row.ManuallyEdited, row.EditedAt.Valid)
	}
	if row.PromptVersion != rerank.ExtractPromptVersion {
		t.Errorf("prompt_version = %q, want %q on create", row.PromptVersion, rerank.ExtractPromptVersion)
	}
	if row.ContentTheme != "积分统计与公布机制" {
		t.Errorf("content_theme = %q", row.ContentTheme)
	}

	// Update again — prompt_version must stay untouched (not re-faked).
	if _, err := svc.store.db.Exec(`UPDATE unit_rerank_semantics SET prompt_version = 'v3' WHERE unit_id = 'ku-1'`); err != nil {
		t.Fatal(err)
	}
	body2 := `{"semantics":{"source_theme":"培训积分管理办法","content_theme":"改","intent":"规则","object":"培训积分","scope":"全体员工"}}`
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("PUT", "/units/ku-1/semantics", bytes.NewBufferString(body2)))
	if w2.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d; body=%s", w2.Code, w2.Body.String())
	}
	row, err = svc.store.GetRerankSemanticsByUnitID("ku-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.PromptVersion != "v3" {
		t.Errorf("prompt_version = %q, want v3 preserved on update", row.PromptVersion)
	}
	if row.ContentTheme != "改" {
		t.Errorf("content_theme = %q, want updated value", row.ContentTheme)
	}
}

func TestPutUnitSemantics_Validation(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)

	for name, body := range map[string]string{
		"no semantics":   `{}`,
		"missing field":  `{"semantics":{"source_theme":"a","content_theme":"b","intent":"c","object":"d","scope":""}}`,
		"malformed json": `{`,
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("PUT", "/units/ku-1/semantics", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestPutUnitSemantics_NonCurrentUnitRejected(t *testing.T) {
	svc, _ := setupCurationTest(t)
	if _, err := svc.store.db.Exec(`UPDATE knowledge_units SET lifecycle = 'superseded' WHERE unit_id = 'ku-1'`); err != nil {
		t.Fatal(err)
	}
	mux := curationMux(svc)

	body := `{"semantics":{"source_theme":"a","content_theme":"b","intent":"c","object":"d","scope":"e"}}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/units/ku-1/semantics", bytes.NewBufferString(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for superseded unit", w.Code)
	}
}

func TestGetUnitSemantics_NotFound(t *testing.T) {
	svc, _ := setupCurationTest(t)
	mux := curationMux(svc)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/units/nope/semantics", nil))
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
