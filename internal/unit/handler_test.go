package unit

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

func TestHandler_ListUnits(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertTestSource(t, db)

	store := NewStore(db)
	store.InsertUnit(&KnowledgeUnit{
		UnitID: "ku-1", SourceID: "src-1", OutlineID: sql.NullString{String: "ol-1", Valid: true},
		Center: "主题一", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1",
	})
	store.InsertUnit(&KnowledgeUnit{
		UnitID: "ku-2", SourceID: "src-1",
		Center: "主题二", LineStart: 6, LineEnd: 10, Status: "completed", PromptVersion: "v1",
	})

	handler := &Handler{svc: &Service{store: store}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sources/src-1/units", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 2 {
		t.Fatalf("got %d units, want 2", len(result))
	}
}

func TestHandler_GetUnit(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertTestSource(t, db)

	store := NewStore(db)
	store.InsertUnit(&KnowledgeUnit{
		UnitID: "ku-1", SourceID: "src-1",
		Center: "主题", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1",
	})
	store.InsertPoint(&KnowledgePoint{
		PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1",
		Content: "知识点", PointType: "definition",
	})

	handler := &Handler{svc: &Service{store: store}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/units/ku-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["center"] != "主题" {
		t.Errorf("center = %v, want 主题", result["center"])
	}

	points, ok := result["points"].([]interface{})
	if !ok || len(points) != 1 {
		t.Errorf("expected 1 point, got %v", result["points"])
	}
}

func TestHandler_ListPoints(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertTestSource(t, db)

	store := NewStore(db)
	store.InsertUnit(&KnowledgeUnit{
		UnitID: "ku-1", SourceID: "src-1",
		Center: "主题", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1",
	})
	store.InsertPoint(&KnowledgePoint{PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1", Content: "A", PointType: "definition"})
	store.InsertPoint(&KnowledgePoint{PointID: "kp-2", UnitID: "ku-1", SourceID: "src-1", Content: "B", PointType: "rule"})

	handler := &Handler{svc: &Service{store: store}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/units/ku-1/points", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 2 {
		t.Errorf("got %d points, want 2", len(result))
	}
}

func TestHandler_ListRelations(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertTestSource(t, db)

	store := NewStore(db)
	store.InsertUnit(&KnowledgeUnit{UnitID: "ku-1", SourceID: "src-1", Center: "A", LineStart: 1, LineEnd: 5, Status: "completed", PromptVersion: "v1"})
	store.InsertUnit(&KnowledgeUnit{UnitID: "ku-2", SourceID: "src-1", Center: "B", LineStart: 6, LineEnd: 10, Status: "completed", PromptVersion: "v1"})
	store.InsertPoint(&KnowledgePoint{PointID: "kp-1", UnitID: "ku-1", SourceID: "src-1", Content: "A", PointType: "definition"})
	store.InsertPoint(&KnowledgePoint{PointID: "kp-2", UnitID: "ku-2", SourceID: "src-1", Content: "B", PointType: "rule"})
	store.InsertRelation(&KnowledgePointRelation{
		RelationID: "rel-1", SourcePointID: "kp-1", TargetPointID: "kp-2",
		RelationType: "related", Direction: "bidirectional", PromptVersion: "v1",
	})

	handler := &Handler{svc: &Service{store: store}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/points/kp-1/relations", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 {
		t.Fatalf("got %d relations, want 1", len(result))
	}
	if result[0]["related_point_id"] != "kp-2" {
		t.Errorf("related_point_id = %v, want kp-2", result[0]["related_point_id"])
	}
	if result[0]["as_source"] != true {
		t.Errorf("as_source = %v, want true", result[0]["as_source"])
	}
}

func TestHandler_GetUnit_NotFound(t *testing.T) {
	db := foundation.NewTestDB(t)
	store := NewStore(db)

	handler := &Handler{svc: &Service{store: store}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/units/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
