package entry

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
// confirm, reject. Confirm remains a manual action for kind=merge and for
// kind=add when Config.AutoConfirmAdd is off; when it's on (the 2026-08-14
// default), the service auto-confirms every freshly created kind=add
// candidate itself, so these endpoints see it mainly as a manual fallback
// (auto-confirm failure, or the flag disabled) rather than the normal path.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /entries/candidates", h.list)
	mux.HandleFunc("GET /entries/candidates/by-domain", h.listByDomain)
	mux.HandleFunc("POST /entries/candidates", h.createManual)
	mux.HandleFunc("POST /entries/domains/{id}/propose-from-orphans", h.proposeFromOrphans)
	mux.HandleFunc("POST /entries/candidates/{id}/confirm", h.confirm)
	mux.HandleFunc("POST /entries/candidates/{id}/reject", h.reject)
	mux.HandleFunc("POST /entries/candidates/{id}/restore", h.restore)
	mux.HandleFunc("DELETE /entries/candidates/{id}", h.delete)
	mux.HandleFunc("GET /entries", h.listEntries)
	mux.HandleFunc("GET /entries/points", h.availablePoints)
	mux.HandleFunc("GET /entries/{id}", h.getEntryDetail)
	mux.HandleFunc("PATCH /entries/{id}", h.updateEntry)
	mux.HandleFunc("POST /entries/{id}/points", h.addEntryPoints)
	mux.HandleFunc("DELETE /entries/{id}/points/{point_id}", h.removeEntryPoint)
}

// availablePoints is GET /entries/points?domain_id=: populates the concept
// candidate confirm dialog's "add KP" picker with entry_id-NULL KPs in the
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
		Description   string   `json:"description"`
		Kind          string   `json:"kind"`
		PointIDs      []string `json:"point_ids"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	candidateID, err := h.svc.CreateManualCandidate(body.DomainID, body.SuggestedName, body.Description, body.Kind, body.PointIDs)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"candidate_id": candidateID})
}

// proposeFromOrphans is the 知识领域页面"+ 新增概念"按钮's endpoint: clusters
// and names domainID's standing entry_id-empty KPs and writes each
// cluster as a pending_confirm add candidate (docs/impl/v1/kpn.md 步骤 3
// on-demand variant) — replaces what used to be a manual-only draft form.
func (h *Handler) proposeFromOrphans(w http.ResponseWriter, r *http.Request) {
	domainID := r.PathValue("id")
	if domainID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "domain id is required")
		return
	}
	touched, err := h.svc.ProposeEntriesFromDomainOrphans(r.Context(), domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]int{"proposals_touched": touched})
}

// listEntries implements GET /entries?domain_id=: populates the concept
// candidate confirm UI's "select an existing concept" picker
// (docs/impl/v1/kpn.md 步骤 6). Not used by any matching/confirm logic.
func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")
	entries, err := h.svc.ListActiveEntries(domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		EntryID   string `json:"entry_id"`
		Name        string `json:"name"`
		DomainID    string `json:"domain_id"`
		Description string `json:"description,omitempty"`
		Kind        string `json:"kind"`
		KPCount     int    `json:"kp_count"`
	}
	items := make([]item, len(entries))
	for i, c := range entries {
		items[i] = item{EntryID: c.EntryID, Name: c.Name, DomainID: c.DomainID, Description: c.Description, Kind: c.Kind, KPCount: c.KPCount}
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

// listByDomain is GET /entries/candidates/by-domain?domain_id=: the 知识领域
// 页面 merged concept grid's data source for pending/rejected/expired kind=add
// candidates targeting one domain — the grid shows these as cards alongside
// real entries instead of a separate status-tabbed list.
func (h *Handler) listByDomain(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")
	if domainID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "domain_id is required")
		return
	}
	views, err := h.svc.ListDomainCandidateViews(domainID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views == nil {
		views = []CandidateView{}
	}
	foundation.WriteJSON(w, http.StatusOK, views)
}

type conceptDetailResp struct {
	EntryID               string                   `json:"entry_id"`
	DomainID              string                   `json:"domain_id"`
	Name                  string                   `json:"name"`
	Description           string                   `json:"description"`
	Kind                  string                   `json:"kind"`
	Points                []conceptDetailPointResp `json:"points"`
	RestorableCandidateID string                   `json:"restorable_candidate_id,omitempty"`
}

type conceptDetailPointResp struct {
	PointID     string `json:"point_id"`
	Content     string `json:"content"`
	SourceID    string `json:"source_id"`
	SourceTitle string `json:"source_title"`
}

func (h *Handler) getEntryDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := h.svc.GetEntryDetail(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		foundation.WriteError(w, http.StatusNotFound, "concept not found")
		return
	}
	resp := conceptDetailResp{EntryID: d.EntryID, DomainID: d.DomainID, Name: d.Name, Description: d.Description, Kind: d.Kind, RestorableCandidateID: d.RestorableCandidateID}
	for _, p := range d.Points {
		resp.Points = append(resp.Points, conceptDetailPointResp{PointID: p.PointID, Content: p.Content, SourceID: p.SourceID, SourceTitle: p.SourceTitle})
	}
	if resp.Points == nil {
		resp.Points = []conceptDetailPointResp{}
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) updateEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := h.svc.UpdateEntryMeta(id, body.Name, body.Description, body.Kind); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"entry_id": id})
}

func (h *Handler) addEntryPoints(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		PointIDs []string `json:"point_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	migrated, err := h.svc.AddEntryPoints(id, body.PointIDs)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]int{"migrated": migrated})
}

func (h *Handler) removeEntryPoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pointID := r.PathValue("point_id")
	unitPointCount, err := h.svc.RemoveEntryPoint(id, pointID)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]int{"unit_point_count": unitPointCount})
}

// confirmRequestBody covers both kinds' fields — Service.Confirm dispatches
// on the candidate's own kind and only consults the relevant subset
// (docs/impl/v1/concept-evolution.md 步骤 3).
type confirmRequestBody struct {
	SuggestedName string   `json:"suggested_name"`
	DomainID      string   `json:"domain_id"`
	Description   string   `json:"description"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	EntryID     string   `json:"entry_id"`
	PointIDs      []string `json:"point_ids"`
}

type confirmResp struct {
	CandidateID  string `json:"candidate_id"`
	Status       string `json:"status"`
	EntryID    string `json:"entry_id,omitempty"`
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
		&ConfirmAddRequest{SuggestedName: body.SuggestedName, DomainID: body.DomainID, Description: body.Description, EntryKind: body.Kind, EntryID: body.EntryID, PointIDs: body.PointIDs},
		&ConfirmMergeRequest{Target: body.Target})
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, confirmResp{
		CandidateID:  result.Candidate.CandidateID,
		Status:       StatusApplied,
		EntryID:    result.EntryID,
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
