package domain

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	mux.HandleFunc("PATCH /domains/{id}", h.update)
	mux.HandleFunc("GET /domains/{id}/doc-categories", h.listDocCategories)
	mux.HandleFunc("POST /domains/{id}/doc-categories", h.createDocCategory)
	mux.HandleFunc("PATCH /doc-categories/{id}", h.updateDocCategory)
	mux.HandleFunc("DELETE /doc-categories/{id}", h.deleteDocCategory)
}

// listDocCategories implements GET /domains/:id/doc-categories: the 知识领域
// page's文档分类 panel data source (docs/design/doc-category.md).
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.svc.Update(id, body.Name, body.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			foundation.WriteError(w, http.StatusNotFound, "domain not found")
			return
		}
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"domain_id": id})
}

func (h *Handler) listDocCategories(w http.ResponseWriter, r *http.Request) {
	domainID := r.PathValue("id")
	categories, err := h.svc.ListDocCategories(domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if categories == nil {
		categories = []DocCategory{}
	}
	foundation.WriteJSON(w, http.StatusOK, categories)
}

func (h *Handler) createDocCategory(w http.ResponseWriter, r *http.Request) {
	domainID := r.PathValue("id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	categoryID, err := h.svc.CreateDocCategory(domainID, body.Name, body.Description)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"category_id": categoryID})
}

func (h *Handler) updateDocCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.svc.UpdateDocCategory(id, body.Name, body.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			foundation.WriteError(w, http.StatusNotFound, "doc category not found")
			return
		}
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"category_id": id})
}

func (h *Handler) deleteDocCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.DeleteDocCategory(id); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"category_id": id})
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
