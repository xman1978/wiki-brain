package retrieval

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /retrieval", h.retrieve)

	// source 详情页"主题标签"人工管理面板（2026-08-27 会话讨论）：底层
	// source_affinity/SubjectNormalizer 都在 retrieval 包里，挂在这个
	// handler 上而不是 source 包的 handler，避免引入新的跨包依赖。
	mux.HandleFunc("GET /sources/{id}/subject-tags", h.listSourceSubjectTags)
	mux.HandleFunc("POST /sources/{id}/subject-tags", h.addSourceSubjectTag)
	mux.HandleFunc("DELETE /sources/{id}/subject-tags/{affinity_id}", h.deleteSourceSubjectTag)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question  string `json:"question"`
		ForceFull bool   `json:"force_full"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Question == "" {
		foundation.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	es, err := h.svc.RetrieveWithProgress(r.Context(), QueryContext{Question: req.Question, ForceFull: req.ForceFull}, nil)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, es)
}

func (h *Handler) listSourceSubjectTags(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	tags, err := h.svc.ListSourceSubjectTags(sourceID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tags == nil {
		tags = []SourceAffinityBinding{}
	}
	foundation.WriteJSON(w, http.StatusOK, tags)
}

func (h *Handler) addSourceSubjectTag(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Subject) == "" {
		foundation.WriteError(w, http.StatusBadRequest, "subject is required")
		return
	}

	binding, err := h.svc.AddSourceSubjectTag(r.Context(), sourceID, req.Subject)
	if err != nil {
		if errors.Is(err, ErrSourceHasNoDomain) {
			foundation.WriteError(w, http.StatusBadRequest, "该来源未分类到任何领域，无法添加主题标签")
			return
		}
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, binding)
}

func (h *Handler) deleteSourceSubjectTag(w http.ResponseWriter, r *http.Request) {
	affinityID := r.PathValue("affinity_id")
	if err := h.svc.RemoveSourceSubjectTag(affinityID); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
