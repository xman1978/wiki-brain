package unit

import (
	"net/http"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /sources/{id}/units", h.triggerExtract)
	mux.HandleFunc("GET /sources/{id}/units", h.listUnits)
	mux.HandleFunc("GET /units/{id}", h.getUnit)
	mux.HandleFunc("GET /units/{id}/points", h.listPoints)
	mux.HandleFunc("GET /points/{id}/relations", h.listRelations)
}

func (h *Handler) triggerExtract(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	if err := h.svc.TriggerExtract(sourceID); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusAccepted, map[string]interface{}{
		"source_id":    sourceID,
		"triggered_at": time.Now().Format(time.RFC3339),
	})
}

func (h *Handler) listUnits(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	units, err := h.svc.store.GetUnitsBySourceID(sourceID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type unitResp struct {
		UnitID    string `json:"unit_id"`
		OutlineID string `json:"outline_id,omitempty"`
		Center    string `json:"center"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Status    string `json:"status"`
	}

	result := make([]unitResp, 0, len(units))
	for _, u := range units {
		r := unitResp{
			UnitID:    u.UnitID,
			Center:    u.Center,
			LineStart: u.LineStart,
			LineEnd:   u.LineEnd,
			Status:    u.Status,
		}
		if u.OutlineID.Valid {
			r.OutlineID = u.OutlineID.String
		}
		result = append(result, r)
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) getUnit(w http.ResponseWriter, r *http.Request) {
	unitID := r.PathValue("id")
	if unitID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing unit id")
		return
	}

	ku, err := h.svc.store.GetUnitByID(unitID)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "unit not found")
		return
	}

	points, err := h.svc.store.GetPointsByUnitID(unitID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type pointResp struct {
		PointID   string `json:"point_id"`
		Content   string `json:"content"`
		PointType string `json:"point_type"`
	}

	type unitDetail struct {
		UnitID    string      `json:"unit_id"`
		SourceID  string      `json:"source_id"`
		OutlineID string      `json:"outline_id,omitempty"`
		ConceptID string      `json:"concept_id,omitempty"`
		Center    string      `json:"center"`
		LineStart int         `json:"line_start"`
		LineEnd   int         `json:"line_end"`
		Status    string      `json:"status"`
		Points    []pointResp `json:"points"`
	}

	resp := unitDetail{
		UnitID:    ku.UnitID,
		SourceID:  ku.SourceID,
		Center:    ku.Center,
		LineStart: ku.LineStart,
		LineEnd:   ku.LineEnd,
		Status:    ku.Status,
		Points:    make([]pointResp, 0, len(points)),
	}
	if ku.OutlineID.Valid {
		resp.OutlineID = ku.OutlineID.String
	}
	if ku.ConceptID.Valid {
		resp.ConceptID = ku.ConceptID.String
	}
	for _, p := range points {
		resp.Points = append(resp.Points, pointResp{
			PointID:   p.PointID,
			Content:   p.Content,
			PointType: p.PointType,
		})
	}

	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) listPoints(w http.ResponseWriter, r *http.Request) {
	unitID := r.PathValue("id")
	if unitID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing unit id")
		return
	}

	points, err := h.svc.store.GetPointsByUnitID(unitID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type pointResp struct {
		PointID   string `json:"point_id"`
		Content   string `json:"content"`
		PointType string `json:"point_type"`
	}

	result := make([]pointResp, 0, len(points))
	for _, p := range points {
		result = append(result, pointResp{
			PointID:   p.PointID,
			Content:   p.Content,
			PointType: p.PointType,
		})
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) listRelations(w http.ResponseWriter, r *http.Request) {
	pointID := r.PathValue("id")
	if pointID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing point id")
		return
	}

	relations, err := h.svc.store.GetRelationsByPointID(pointID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type relResp struct {
		RelationID          string `json:"relation_id"`
		RelatedPointID      string `json:"related_point_id"`
		RelatedPointContent string `json:"related_point_content,omitempty"`
		RelationType        string `json:"relation_type"`
		Direction           string `json:"direction"`
		AsSource            bool   `json:"as_source"`
	}

	result := make([]relResp, 0, len(relations))
	for _, rel := range relations {
		relatedID := rel.TargetPointID
		asSource := true
		if rel.SourcePointID != pointID {
			relatedID = rel.SourcePointID
			asSource = false
		}

		resp := relResp{
			RelationID:     rel.RelationID,
			RelatedPointID: relatedID,
			RelationType:   rel.RelationType,
			Direction:      rel.Direction,
			AsSource:       asSource,
		}

		result = append(result, resp)
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}
