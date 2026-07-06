package concept

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

// RegisterRoutes implements docs/impl/v1/concept-evolution.md 步骤 3: list,
// confirm, reject. There is no auto mode — confirm is always a human action.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /concepts/candidates", h.list)
	mux.HandleFunc("POST /concepts/candidates/{id}/confirm", h.confirm)
	mux.HandleFunc("POST /concepts/candidates/{id}/reject", h.reject)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	views, err := h.svc.ListCandidateViews(status)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views == nil {
		views = []CandidateView{}
	}
	foundation.WriteJSON(w, http.StatusOK, views)
}

// confirmRequestBody covers both kinds' fields — Service.Confirm dispatches
// on the candidate's own kind and only consults the relevant subset
// (docs/impl/v1/concept-evolution.md 步骤 3).
type confirmRequestBody struct {
	SuggestedName string `json:"suggested_name"`
	DomainID      string `json:"domain_id"`
	Target        string `json:"target"`
}

type confirmResp struct {
	CandidateID  string `json:"candidate_id"`
	Status       string `json:"status"`
	ConceptID    string `json:"concept_id,omitempty"`
	MigratedKUs  int    `json:"migrated_kus"`
	FlaggedPages int    `json:"flagged_pages,omitempty"`
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body confirmRequestBody
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	result, err := h.svc.Confirm(id,
		&ConfirmAddRequest{SuggestedName: body.SuggestedName, DomainID: body.DomainID},
		&ConfirmMergeRequest{Target: body.Target})
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, confirmResp{
		CandidateID:  result.Candidate.CandidateID,
		Status:       StatusApplied,
		ConceptID:    result.ConceptID,
		MigratedKUs:  result.MigratedKUs,
		FlaggedPages: result.FlaggedPages,
	})
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Reject(id); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"candidate_id": id,
		"status":       StatusRejected,
	})
}
