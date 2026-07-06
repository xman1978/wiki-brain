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
	mux.HandleFunc("POST /sources/{id}/kpn-cross", h.triggerCrossKPN)
	mux.HandleFunc("GET /units/{id}", h.getUnit)
	mux.HandleFunc("GET /units/{id}/points", h.listPoints)
	mux.HandleFunc("GET /points/{id}", h.getPoint)
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

// triggerCrossKPN implements POST /sources/:id/kpn-cross
// (docs/impl/v1/kpn.md 步骤 7): manual backfill of cross-Source KPN matching
// for an existing Source. Idempotent — re-running never creates duplicate
// relations (idx_kp_relations_uniq).
func (h *Handler) triggerCrossKPN(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	result, err := h.svc.CrossSourceKPN(r.Context(), sourceID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) listUnits(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	lifecycle := r.URL.Query().Get("lifecycle")
	units, err := h.svc.store.GetUnitsBySourceIDFiltered(sourceID, lifecycle)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type unitResp struct {
		UnitID             string  `json:"unit_id"`
		OutlineID          string  `json:"outline_id,omitempty"`
		Center             string  `json:"center"`
		LineStart          int     `json:"line_start"`
		LineEnd            int     `json:"line_end"`
		Status             string  `json:"status"`
		Lifecycle          string  `json:"lifecycle"`
		LifecycleChangedAt *string `json:"lifecycle_changed_at,omitempty"`
	}

	result := make([]unitResp, 0, len(units))
	for _, u := range units {
		r := unitResp{
			UnitID:    u.UnitID,
			Center:    u.Center,
			LineStart: u.LineStart,
			LineEnd:   u.LineEnd,
			Status:    u.Status,
			Lifecycle: u.Lifecycle,
		}
		if u.OutlineID.Valid {
			r.OutlineID = u.OutlineID.String
		}
		if u.LifecycleChangedAt.Valid {
			t := u.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
			r.LifecycleChangedAt = &t
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
		PointID            string  `json:"point_id"`
		Content            string  `json:"content"`
		PointType          string  `json:"point_type"`
		Lifecycle          string  `json:"lifecycle"`
		LifecycleChangedAt *string `json:"lifecycle_changed_at,omitempty"`
	}

	type unitDetail struct {
		UnitID             string      `json:"unit_id"`
		SourceID           string      `json:"source_id"`
		OutlineID          string      `json:"outline_id,omitempty"`
		ConceptID          string      `json:"concept_id,omitempty"`
		Center             string      `json:"center"`
		LineStart          int         `json:"line_start"`
		LineEnd            int         `json:"line_end"`
		Status             string      `json:"status"`
		Lifecycle          string      `json:"lifecycle"`
		LifecycleChangedAt *string     `json:"lifecycle_changed_at,omitempty"`
		Points             []pointResp `json:"points"`
	}

	resp := unitDetail{
		UnitID:    ku.UnitID,
		SourceID:  ku.SourceID,
		Center:    ku.Center,
		LineStart: ku.LineStart,
		LineEnd:   ku.LineEnd,
		Status:    ku.Status,
		Lifecycle: ku.Lifecycle,
		Points:    make([]pointResp, 0, len(points)),
	}
	if ku.OutlineID.Valid {
		resp.OutlineID = ku.OutlineID.String
	}
	if ku.ConceptID.Valid {
		resp.ConceptID = ku.ConceptID.String
	}
	if ku.LifecycleChangedAt.Valid {
		t := ku.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
		resp.LifecycleChangedAt = &t
	}
	for _, p := range points {
		pr := pointResp{
			PointID:   p.PointID,
			Content:   p.Content,
			PointType: p.PointType,
			Lifecycle: p.Lifecycle,
		}
		if p.LifecycleChangedAt.Valid {
			t := p.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
			pr.LifecycleChangedAt = &t
		}
		resp.Points = append(resp.Points, pr)
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
		PointID            string  `json:"point_id"`
		Content            string  `json:"content"`
		PointType          string  `json:"point_type"`
		Lifecycle          string  `json:"lifecycle"`
		LifecycleChangedAt *string `json:"lifecycle_changed_at,omitempty"`
	}

	result := make([]pointResp, 0, len(points))
	for _, p := range points {
		pr := pointResp{
			PointID:   p.PointID,
			Content:   p.Content,
			PointType: p.PointType,
			Lifecycle: p.Lifecycle,
		}
		if p.LifecycleChangedAt.Valid {
			t := p.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
			pr.LifecycleChangedAt = &t
		}
		result = append(result, pr)
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) getPoint(w http.ResponseWriter, r *http.Request) {
	pointID := r.PathValue("id")
	if pointID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing point id")
		return
	}

	kp, err := h.svc.store.GetPointByID(pointID)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "point not found")
		return
	}

	type pointDetail struct {
		PointID            string  `json:"point_id"`
		UnitID             string  `json:"unit_id"`
		SourceID           string  `json:"source_id"`
		Content            string  `json:"content"`
		PointType          string  `json:"point_type"`
		Lifecycle          string  `json:"lifecycle"`
		LifecycleChangedAt *string `json:"lifecycle_changed_at,omitempty"`
	}

	resp := pointDetail{
		PointID:   kp.PointID,
		UnitID:    kp.UnitID,
		SourceID:  kp.SourceID,
		Content:   kp.Content,
		PointType: kp.PointType,
		Lifecycle: kp.Lifecycle,
	}
	if kp.LifecycleChangedAt.Valid {
		t := kp.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
		resp.LifecycleChangedAt = &t
	}

	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) listRelations(w http.ResponseWriter, r *http.Request) {
	pointID := r.PathValue("id")
	if pointID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing point id")
		return
	}

	scope := r.URL.Query().Get("scope")
	relations, err := h.svc.store.GetRelationsByPointID(pointID, scope)
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
		Scope               string `json:"scope"`
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
			Scope:          rel.Scope,
			AsSource:       asSource,
		}

		result = append(result, resp)
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}
