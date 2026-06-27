package study

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /study/run", h.runStudy)
	mux.HandleFunc("GET /study/reports", h.listReports)
	mux.HandleFunc("GET /study/reports/latest", h.getLatestReport)
	mux.HandleFunc("GET /study/reports/{id}", h.getReport)
	mux.HandleFunc("GET /study/candidates", h.listCandidates)
	mux.HandleFunc("GET /study/gaps", h.listGaps)
}

func (h *Handler) runStudy(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.Run()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) listReports(w http.ResponseWriter, r *http.Request) {
	reports, err := h.svc.store.ListReports()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reports == nil {
		reports = []ReportMeta{}
	}
	foundation.WriteJSON(w, http.StatusOK, reports)
}

func (h *Handler) getReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		foundation.WriteError(w, http.StatusBadRequest, "report id is required")
		return
	}

	content, err := h.svc.store.GetReport(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if content == nil {
		foundation.WriteError(w, http.StatusNotFound, "report not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(*content))
}

func (h *Handler) getLatestReport(w http.ResponseWriter, r *http.Request) {
	content, err := h.svc.store.GetLatestReport()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if content == nil {
		foundation.WriteError(w, http.StatusNotFound, "no reports available")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(*content))
}

func (h *Handler) listCandidates(w http.ResponseWriter, r *http.Request) {
	recommendation := r.URL.Query().Get("recommendation")
	limit := queryInt(r, "limit", 50)

	candidates, err := h.svc.buildActivationCandidates(h.svc.cfg.ReportPeriodDays)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if recommendation != "" {
		var filtered []ActivationLinkCandidate
		for _, c := range candidates {
			if c.Recommendation == recommendation {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	if candidates == nil {
		candidates = []ActivationLinkCandidate{}
	}
	foundation.WriteJSON(w, http.StatusOK, candidates)
}

func (h *Handler) listGaps(w http.ResponseWriter, r *http.Request) {
	minHitCount := queryInt(r, "min_hit_count", 0)
	limit := queryInt(r, "limit", 50)

	gaps, err := h.svc.store.ListGaps(minHitCount, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]KnowledgeGapEntry, len(gaps))
	for i, g := range gaps {
		result[i] = KnowledgeGapEntry{
			QuestionTerms:  g.QuestionTerms,
			Question:       g.Question,
			HitCount:       g.HitCount,
			Recommendation: "补充材料",
		}
	}
	foundation.WriteJSON(w, http.StatusOK, result)
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

// for raw JSON responses
func writeRawJSON(w http.ResponseWriter, status int, data json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}
