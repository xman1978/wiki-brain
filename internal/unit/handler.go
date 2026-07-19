package unit

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/rerank"
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
	mux.HandleFunc("GET /sources/{id}/coverage", h.getCoverage)
	mux.HandleFunc("POST /sources/{id}/coverage/fix", h.fixCoverageGap)
	mux.HandleFunc("GET /sources/{id}/coverage/merge-target", h.previewMergeTarget)
	mux.HandleFunc("POST /sources/{id}/coverage/merge", h.mergeCoverageGap)
	mux.HandleFunc("POST /sources/{id}/kpn-cross", h.triggerCrossKPN)
	mux.HandleFunc("GET /units/{id}", h.getUnit)
	mux.HandleFunc("GET /units/{id}/semantics", h.getUnitSemantics)
	mux.HandleFunc("PUT /units/{id}/semantics", h.putUnitSemantics)
	mux.HandleFunc("GET /units/{id}/points", h.listPoints)
	mux.HandleFunc("POST /units/{id}/points", h.addPoint)
	mux.HandleFunc("GET /points/{id}", h.getPoint)
	mux.HandleFunc("PUT /points/{id}", h.updatePoint)
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

// getCoverage implements GET /sources/:id/coverage — a read-only diagnostic
// (no LLM calls, no writes) reporting which lines of the source's current
// content ended up in no completed KnowledgeUnit at all. See
// unit.ComputeCoverage for what counts as covered.
func (h *Handler) getCoverage(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	report, err := h.svc.SourceCoverageReport(sourceID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	totalLines, coveredLines := 0, 0
	for _, seg := range report {
		totalLines += seg.TotalLines
		coveredLines += seg.CoveredLines
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id":     sourceID,
		"total_lines":   totalLines,
		"covered_lines": coveredLines,
		"segments":      report,
	})
}

// fixCoverageGap implements POST /sources/:id/coverage/fix — manually
// recovers one gap surfaced by GET /sources/:id/coverage by re-running point
// and rerank-semantics extraction for exactly that line range and inserting
// it as a new standalone current knowledge unit (see Service.FixCoverageGap).
func (h *Handler) fixCoverageGap(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	var body struct {
		LineStart int `json:"line_start"`
		LineEnd   int `json:"line_end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.LineStart < 1 || body.LineEnd < body.LineStart {
		foundation.WriteError(w, http.StatusBadRequest, "invalid line range")
		return
	}

	ku, err := h.svc.FixCoverageGap(r.Context(), sourceID, body.LineStart, body.LineEnd)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"unit_id":    ku.UnitID,
		"center":     ku.Center,
		"line_start": ku.LineStart,
		"line_end":   ku.LineEnd,
	})
}

// previewMergeTarget implements GET /sources/:id/coverage/merge-target —
// read-only lookup of which neighbor unit POST .../coverage/merge would
// widen for the same (line_start, line_end, direction), so the frontend can
// show its content and ask for confirmation before actually merging.
func (h *Handler) previewMergeTarget(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	lineStart, errStart := strconv.Atoi(r.URL.Query().Get("line_start"))
	lineEnd, errEnd := strconv.Atoi(r.URL.Query().Get("line_end"))
	direction := r.URL.Query().Get("direction")
	if errStart != nil || errEnd != nil || lineStart < 1 || lineEnd < lineStart {
		foundation.WriteError(w, http.StatusBadRequest, "invalid line range")
		return
	}
	if direction != MergeDirectionPrev && direction != MergeDirectionNext {
		foundation.WriteError(w, http.StatusBadRequest, "direction must be \"prev\" or \"next\"")
		return
	}

	preview, err := h.svc.PreviewMergeTarget(sourceID, lineStart, lineEnd, direction)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, preview)
}

// mergeCoverageGap implements POST /sources/:id/coverage/merge — manually
// recovers one gap surfaced by GET /sources/:id/coverage by absorbing it
// into a neighboring knowledge unit's line range (see
// Service.MergeCoverageGap), for content too fragmentary to deserve its own
// unit (e.g. single /etc/hosts-style lines).
func (h *Handler) mergeCoverageGap(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	var body struct {
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Direction string `json:"direction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.LineStart < 1 || body.LineEnd < body.LineStart {
		foundation.WriteError(w, http.StatusBadRequest, "invalid line range")
		return
	}
	if body.Direction != MergeDirectionPrev && body.Direction != MergeDirectionNext {
		foundation.WriteError(w, http.StatusBadRequest, "direction must be \"prev\" or \"next\"")
		return
	}

	ku, err := h.svc.MergeCoverageGap(r.Context(), sourceID, body.LineStart, body.LineEnd, body.Direction)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"unit_id":    ku.UnitID,
		"center":     ku.Center,
		"line_start": ku.LineStart,
		"line_end":   ku.LineEnd,
	})
}

// getUnitSemantics / putUnitSemantics: rerank 语义人工修正
// (docs/impl/v1/semantics-curation.md)。GET 返回 KU 本体（只读，含正文切片）
// 与语义行（missing 时为 null）；PUT 只接受 semantics —— KU 本体不可编辑。
func (h *Handler) getUnitSemantics(w http.ResponseWriter, r *http.Request) {
	unitID := r.PathValue("id")
	if unitID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing unit id")
		return
	}

	view, err := h.svc.GetUnitSemanticsView(unitID)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "unit not found")
		return
	}

	type semanticsResp struct {
		SourceTheme    string  `json:"source_theme"`
		ContentTheme   string  `json:"content_theme"`
		Intent         string  `json:"intent"`
		Object         string  `json:"object"`
		Scope          string  `json:"scope"`
		PromptVersion  string  `json:"prompt_version"`
		ManuallyEdited bool    `json:"manually_edited"`
		EditedAt       *string `json:"edited_at"`
	}
	type unitResp struct {
		UnitID          string `json:"unit_id"`
		SourceID        string `json:"source_id"`
		Center          string `json:"center"`
		LineStart       int    `json:"line_start"`
		LineEnd         int    `json:"line_end"`
		Lifecycle       string `json:"lifecycle"`
		Content         string `json:"content"`
		OutlinePath     string `json:"outline_path,omitempty"`
		OutlineNodeType string `json:"outline_node_type,omitempty"`
	}

	resp := struct {
		Unit      unitResp       `json:"unit"`
		Semantics *semanticsResp `json:"semantics"`
	}{
		Unit: unitResp{
			UnitID:          view.Unit.UnitID,
			SourceID:        view.Unit.SourceID,
			Center:          view.Unit.Center,
			LineStart:       view.Unit.LineStart,
			LineEnd:         view.Unit.LineEnd,
			Lifecycle:       view.Unit.Lifecycle,
			Content:         view.Unit.Content,
			OutlinePath:     view.Unit.OutlinePath,
			OutlineNodeType: view.Unit.OutlineNodeType,
		},
	}
	if view.Semantics != nil {
		sem := &semanticsResp{
			SourceTheme:    view.Semantics.SourceTheme,
			ContentTheme:   view.Semantics.ContentTheme,
			Intent:         view.Semantics.Intent,
			Object:         view.Semantics.Object,
			Scope:          view.Semantics.Scope,
			PromptVersion:  view.Semantics.PromptVersion,
			ManuallyEdited: view.Semantics.ManuallyEdited,
		}
		if view.Semantics.EditedAt.Valid {
			t := view.Semantics.EditedAt.Time.UTC().Format(time.RFC3339)
			sem.EditedAt = &t
		}
		resp.Semantics = sem
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) putUnitSemantics(w http.ResponseWriter, r *http.Request) {
	unitID := r.PathValue("id")
	if unitID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing unit id")
		return
	}

	var req struct {
		Semantics *struct {
			SourceTheme  string `json:"source_theme"`
			ContentTheme string `json:"content_theme"`
			Intent       string `json:"intent"`
			Object       string `json:"object"`
			Scope        string `json:"scope"`
		} `json:"semantics"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Semantics == nil {
		foundation.WriteError(w, http.StatusBadRequest, "semantics is required")
		return
	}

	sem := req.Semantics
	// 校验规则与 unit_semantics_extract.md 的 Schema 一致：五字段均非空 ——
	// 人工数据与 LLM 数据形状相同。
	for name, v := range map[string]string{
		"source_theme": sem.SourceTheme, "content_theme": sem.ContentTheme,
		"intent": sem.Intent, "object": sem.Object, "scope": sem.Scope,
	} {
		if strings.TrimSpace(v) == "" {
			foundation.WriteError(w, http.StatusBadRequest, name+" is required")
			return
		}
	}

	err := h.svc.UpdateUnitSemantics(unitID, rerank.Semantics{
		UnitID:       unitID,
		SourceTheme:  sem.SourceTheme,
		ContentTheme: sem.ContentTheme,
		Intent:       sem.Intent,
		Object:       sem.Object,
		Scope:        sem.Scope,
	})
	if errors.Is(err, ErrUnitNotCurrent) {
		foundation.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "unit not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		ManuallyEdited     bool    `json:"manually_edited"`
		EditedAt           *string `json:"edited_at,omitempty"`
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
			PointID:        p.PointID,
			Content:        p.Content,
			PointType:      p.PointType,
			Lifecycle:      p.Lifecycle,
			ManuallyEdited: p.ManuallyEdited,
		}
		if p.LifecycleChangedAt.Valid {
			t := p.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
			pr.LifecycleChangedAt = &t
		}
		if p.EditedAt.Valid {
			t := p.EditedAt.Time.UTC().Format(time.RFC3339)
			pr.EditedAt = &t
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
		ManuallyEdited     bool    `json:"manually_edited"`
		EditedAt           *string `json:"edited_at,omitempty"`
	}

	result := make([]pointResp, 0, len(points))
	for _, p := range points {
		pr := pointResp{
			PointID:        p.PointID,
			Content:        p.Content,
			PointType:      p.PointType,
			Lifecycle:      p.Lifecycle,
			ManuallyEdited: p.ManuallyEdited,
		}
		if p.LifecycleChangedAt.Valid {
			t := p.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
			pr.LifecycleChangedAt = &t
		}
		if p.EditedAt.Valid {
			t := p.EditedAt.Time.UTC().Format(time.RFC3339)
			pr.EditedAt = &t
		}
		result = append(result, pr)
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

// addPoint implements POST /units/:id/points — 人工新增 KP
// (docs/impl/v1/semantics-curation.md "KP 人工修正")，取代旧版 key_facts
// 人工编辑。成功后同步触发该 KP 的增量 KPN 分析。
func (h *Handler) addPoint(w http.ResponseWriter, r *http.Request) {
	unitID := r.PathValue("id")
	if unitID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing unit id")
		return
	}

	var req struct {
		Content   string `json:"content"`
		PointType string `json:"point_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.svc.AddManualPoint(r.Context(), unitID, req.Content, req.PointType)
	if errors.Is(err, ErrUnitNotCurrent) {
		foundation.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, ErrInvalidPointType) || errors.Is(err, ErrEmptyPointContent) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		foundation.WriteError(w, http.StatusNotFound, "unit not found")
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"point_id":          result.Point.PointID,
		"unit_id":           result.Point.UnitID,
		"content":           result.Point.Content,
		"point_type":        result.Point.PointType,
		"manually_edited":   true,
		"relations_created": result.RelationsCreated,
	})
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
		ManuallyEdited     bool    `json:"manually_edited"`
		EditedAt           *string `json:"edited_at,omitempty"`
	}

	resp := pointDetail{
		PointID:        kp.PointID,
		UnitID:         kp.UnitID,
		SourceID:       kp.SourceID,
		Content:        kp.Content,
		PointType:      kp.PointType,
		Lifecycle:      kp.Lifecycle,
		ManuallyEdited: kp.ManuallyEdited,
	}
	if kp.LifecycleChangedAt.Valid {
		t := kp.LifecycleChangedAt.Time.Format("2006-01-02T15:04:05Z")
		resp.LifecycleChangedAt = &t
	}
	if kp.EditedAt.Valid {
		t := kp.EditedAt.Time.UTC().Format(time.RFC3339)
		resp.EditedAt = &t
	}

	foundation.WriteJSON(w, http.StatusOK, resp)
}

// updatePoint implements PUT /points/:id — 人工编辑已有 KP 的 content/
// point_type（docs/impl/v1/semantics-curation.md "KP 人工修正"）。不触发
// KPN 重跑，理由见 Service.UpdateManualPoint 的注释。
func (h *Handler) updatePoint(w http.ResponseWriter, r *http.Request) {
	pointID := r.PathValue("id")
	if pointID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing point id")
		return
	}

	var req struct {
		Content   string `json:"content"`
		PointType string `json:"point_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.svc.UpdateManualPoint(pointID, req.Content, req.PointType)
	if errors.Is(err, ErrUnitNotCurrent) {
		foundation.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, ErrInvalidPointType) || errors.Is(err, ErrEmptyPointContent) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		foundation.WriteError(w, http.StatusNotFound, "point not found")
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
