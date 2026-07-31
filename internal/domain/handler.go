package domain

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
	mux.HandleFunc("GET /domains", h.list)
	mux.HandleFunc("POST /domains", h.create)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	domains, err := h.svc.List()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if domains == nil {
		domains = []Domain{}
	}
	foundation.WriteJSON(w, http.StatusOK, domains)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	domainID, err := h.svc.Create(body.Name, body.Description)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"domain_id": domainID})
}
