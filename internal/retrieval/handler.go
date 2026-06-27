package retrieval

import (
	"encoding/json"
	"net/http"

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
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Question == "" {
		foundation.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	es, err := h.svc.Retrieve(r.Context(), req.Question)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, es)
}
