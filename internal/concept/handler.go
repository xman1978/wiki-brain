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
	mux.HandleFunc("POST /concepts/candidates", h.createManual)
	mux.HandleFunc("POST /concepts/candidates/{id}/confirm", h.confirm)
	mux.HandleFunc("POST /concepts/candidates/{id}/reject", h.reject)
	mux.HandleFunc("POST /concepts/candidates/{id}/restore", h.restore)
	mux.HandleFunc("DELETE /concepts/candidates/{id}", h.delete)
	mux.HandleFunc("GET /concepts", h.listConcepts)
	mux.HandleFunc("GET /concepts/points", h.availablePoints)
}

// availablePoints is GET /concepts/points?domain_id=: populates the concept
// candidate confirm dialog's "add KP" picker with concept_id-NULL KPs in the
// given domain — the same eligibility precondition ConfirmAdd/ConfirmAssign's
// migration query enforces.
func (h *Handler) availablePoints(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")
	if domainID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "domain_id is required")
		return
	}
	points, err := h.svc.AvailablePoints(domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		PointID     string `json:"point_id"`
		Content     string `json:"content"`
		SourceID    string `json:"source_id"`
		SourceTitle string `json:"source_title"`
	}
	items := make([]item, len(points))
	for i, p := range points {
		items[i] = item{PointID: p.PointID, Content: p.Content, SourceID: p.SourceID, SourceTitle: p.SourceTitle}
	}
	foundation.WriteJSON(w, http.StatusOK, items)
}

// createManual is what the concept evolution UI's manual draft dialog calls
// on "确认" (save as pending_confirm) or "驳回" (save, then the frontend
// immediately calls reject on the returned id) — the "新增" button itself
// doesn't call this, it just opens the draft form client-side.
func (h *Handler) createManual(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DomainID      string   `json:"domain_id"`
		SuggestedName string   `json:"suggested_name"`
		PointIDs      []string `json:"point_ids"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	candidateID, err := h.svc.CreateManualCandidate(body.DomainID, body.SuggestedName, body.PointIDs)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"candidate_id": candidateID})
}

// listConcepts implements GET /concepts?domain_id=: populates the concept
// candidate confirm UI's "select an existing concept" picker
// (docs/impl/v1/kpn.md 步骤 6). Not used by any matching/confirm logic.
func (h *Handler) listConcepts(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")
	concepts, err := h.svc.ListActiveConcepts(domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		ConceptID string `json:"concept_id"`
		Name      string `json:"name"`
		DomainID  string `json:"domain_id"`
	}
	items := make([]item, len(concepts))
	for i, c := range concepts {
		items[i] = item{ConceptID: c.ConceptID, Name: c.Name, DomainID: c.DomainID}
	}
	foundation.WriteJSON(w, http.StatusOK, items)
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
	SuggestedName string   `json:"suggested_name"`
	DomainID      string   `json:"domain_id"`
	Target        string   `json:"target"`
	ConceptID     string   `json:"concept_id"`
	PointIDs      []string `json:"point_ids"`
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
		&ConfirmAddRequest{SuggestedName: body.SuggestedName, DomainID: body.DomainID, ConceptID: body.ConceptID, PointIDs: body.PointIDs},
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

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Restore(id); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"candidate_id": id,
		"status":       StatusPendingConfirm,
	})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Delete(id); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"candidate_id": id})
}
