package trace

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
	mux.HandleFunc("GET /traces", h.listTraces)
	mux.HandleFunc("GET /traces/{id}", h.getTrace)
	mux.HandleFunc("POST /traces/{id}/feedback", h.postFeedback)
	mux.HandleFunc("GET /cooccurrence", h.listCooccurrence)
	mux.HandleFunc("GET /learning-events", h.listLearningEvents)
}

func (h *Handler) listTraces(w http.ResponseWriter, r *http.Request) {
	quality := r.URL.Query().Get("quality")
	answerID := r.URL.Query().Get("answer_id")
	limit := queryInt(r, "limit", 20)
	offset := queryInt(r, "offset", 0)

	traces, err := h.svc.store.ListTraces(quality, answerID, limit, offset)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if traces == nil {
		traces = []Trace{}
	}
	foundation.WriteJSON(w, http.StatusOK, traces)
}

func (h *Handler) getTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		foundation.WriteError(w, http.StatusBadRequest, "trace id is required")
		return
	}

	t, err := h.svc.store.GetTrace(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		foundation.WriteError(w, http.StatusNotFound, "trace not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, t)
}

func (h *Handler) postFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		foundation.WriteError(w, http.StatusBadRequest, "trace id is required")
		return
	}

	var req FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Type != "positive" && req.Type != "negative" && req.Type != "correction" {
		foundation.WriteError(w, http.StatusBadRequest, "type must be positive, negative, or correction")
		return
	}

	t, err := h.svc.store.GetTrace(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		foundation.WriteError(w, http.StatusNotFound, "trace not found")
		return
	}

	if err := h.svc.SubmitFeedback(id, req); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) listCooccurrence(w http.ResponseWriter, r *http.Request) {
	pointID := r.URL.Query().Get("point_id")
	minConfidentCount := queryInt(r, "min_confident_count", 0)
	limit := queryInt(r, "limit", 50)

	results, err := h.svc.store.ListCooccurrence(pointID, minConfidentCount, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []Cooccurrence{}
	}
	foundation.WriteJSON(w, http.StatusOK, results)
}

func (h *Handler) listLearningEvents(w http.ResponseWriter, r *http.Request) {
	eventType := r.URL.Query().Get("type")
	processed := queryInt(r, "processed", -1)
	limit := queryInt(r, "limit", 20)

	events, err := h.svc.store.ListLearningEvents(eventType, processed, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []LearningEvent{}
	}
	foundation.WriteJSON(w, http.StatusOK, events)
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
