package sysconfig

import (
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
	mux.HandleFunc("GET /system/fileview", h.getFileView)
	mux.HandleFunc("PUT /system/fileview", h.putFileView)
	mux.HandleFunc("GET /system/session", h.getSession)
	mux.HandleFunc("PUT /system/session", h.putSession)
}

func (h *Handler) getFileView(w http.ResponseWriter, _ *http.Request) {
	v, err := h.svc.GetFileView()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) putFileView(w http.ResponseWriter, r *http.Request) {
	var v FileViewSettings
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	saved, err := h.svc.SaveFileView(v)
	if errors.Is(err, ErrInvalidInput) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, saved)
}

func (h *Handler) getSession(w http.ResponseWriter, _ *http.Request) {
	v, err := h.svc.GetSession()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) putSession(w http.ResponseWriter, r *http.Request) {
	var v SessionSettings
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	saved, err := h.svc.SaveSession(v)
	if errors.Is(err, ErrInvalidInput) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, saved)
}
