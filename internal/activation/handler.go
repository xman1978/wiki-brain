package activation

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /activation-links", h.list)
	mux.HandleFunc("GET /activation-links/{id}", h.get)
	mux.HandleFunc("GET /activation-links/{id}/questions", h.questions)
	mux.HandleFunc("GET /activation-links/{id}/learning-results", h.learningResults)
	mux.HandleFunc("POST /activation-links/{id}/confirm", h.confirm)
	mux.HandleFunc("POST /activation-links/{id}/reject", h.reject)

	mux.HandleFunc("GET /subject-synonyms", h.listSynonyms)
	mux.HandleFunc("GET /subject-synonyms/{id}", h.getSynonym)
	mux.HandleFunc("POST /subject-synonyms/{id}/confirm", h.confirmSynonym)
	mux.HandleFunc("POST /subject-synonyms/{id}/reject", h.rejectSynonym)
}

type linkResp struct {
	LinkID             string              `json:"link_id"`
	QuestionTerms      string              `json:"question_terms"`
	SubjectTerms       string              `json:"subject_terms,omitempty"`
	IntentTerms        []string            `json:"intent_terms"`
	Audience           []string            `json:"audience"`
	ConstraintTerms    []string            `json:"constraint_terms"`
	ObservedConditions []ObservedCondition `json:"observed_conditions"`
	PointID            string              `json:"point_id"`
	PointSummary       string              `json:"point_summary,omitempty"`
	UnitCenter         string              `json:"unit_center,omitempty"`
	Status             string              `json:"status"`
	AdoptCount         int                 `json:"adopt_count"`
	FailCount          int                 `json:"fail_count"`
	LastUsedAt         *string             `json:"last_used_at,omitempty"`
	CreatedAt          string              `json:"created_at"`
}

func toLinkResp(l ActivationLink, pointSummary, unitCenter string) linkResp {
	r := linkResp{
		LinkID:             l.LinkID,
		QuestionTerms:      l.QuestionTerms,
		SubjectTerms:       l.SubjectTerms,
		IntentTerms:        l.IntentTerms,
		Audience:           l.Audience,
		ConstraintTerms:    l.ConstraintTerms,
		ObservedConditions: l.ObservedConditions,
		PointID:            l.PointID,
		PointSummary:       pointSummary,
		UnitCenter:         unitCenter,
		Status:             l.Status,
		AdoptCount:         l.AdoptCount,
		FailCount:          l.FailCount,
		CreatedAt:          l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if r.ObservedConditions == nil {
		r.ObservedConditions = []ObservedCondition{}
	}
	if l.LastUsedAt.Valid {
		s := l.LastUsedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		r.LastUsedAt = &s
	}
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	var pointIDs []string
	if raw := r.URL.Query().Get("point_ids"); raw != "" {
		pointIDs = strings.Split(raw, ",")
	}
	defaultLimit := 50
	if len(pointIDs) > 0 {
		defaultLimit = 0 // let the store pick its bulk-sized default (see ListLinksFilter.PointIDs doc)
	}
	f := ListLinksFilter{
		Status:   r.URL.Query().Get("status"),
		PointID:  r.URL.Query().Get("point_id"),
		PointIDs: pointIDs,
		Limit:    queryInt(r, "limit", defaultLimit),
		Offset:   queryInt(r, "offset", 0),
	}

	rows, err := h.svc.ListLinks(f)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]linkResp, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, toLinkResp(row.ActivationLink, row.PointSummary, row.UnitCenter))
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

type linkDetailResp struct {
	linkResp
	Scene                string   `json:"scene,omitempty"`
	Goal                 string   `json:"goal,omitempty"`
	UnitID               string   `json:"unit_id,omitempty"`
	SourceTitle          string   `json:"source_title,omitempty"`
	CreatedFrom          []string `json:"created_from"`
	PendingPromoteReason string   `json:"pending_promote_reason,omitempty"`
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := h.svc.GetLink(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if link == nil {
		foundation.WriteError(w, http.StatusNotFound, "activation link not found")
		return
	}

	var createdFrom []string
	if err := json.Unmarshal([]byte(link.CreatedFrom), &createdFrom); err != nil {
		createdFrom = []string{}
	}

	pointContent, unitID, unitCenter, sourceTitle, err := h.svc.Store().PointUnitInfo(link.PointID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := linkDetailResp{
		linkResp:    toLinkResp(*link, pointContent, unitCenter),
		Scene:       link.Scene,
		Goal:        link.Goal,
		UnitID:      unitID,
		SourceTitle: sourceTitle,
		CreatedFrom: createdFrom,
	}
	if pending, err := h.svc.Store().FindPendingPromote(id); err == nil && pending != nil {
		resp.PendingPromoteReason = pending.Reason
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) questions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	qs, err := h.svc.ListLinkQuestions(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if qs == nil {
		foundation.WriteError(w, http.StatusNotFound, "activation link not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, qs)
}

type learningResultResp struct {
	ResultID    string `json:"result_id"`
	Action      string `json:"action"`
	ObjectType  string `json:"object_type"`
	ObjectID    string `json:"object_id"`
	Reason      string `json:"reason"`
	EventIDs    string `json:"event_ids"`
	Status      string `json:"status"`
	ConfirmedBy string `json:"confirmed_by,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (h *Handler) learningResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := h.svc.GetLink(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if link == nil {
		foundation.WriteError(w, http.StatusNotFound, "activation link not found")
		return
	}
	results, err := h.svc.ListLearningResults(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]learningResultResp, 0, len(results))
	for _, lr := range results {
		item := learningResultResp{
			ResultID:   lr.ResultID,
			Action:     lr.Action,
			ObjectType: lr.ObjectType,
			ObjectID:   lr.ObjectID,
			Reason:     lr.Reason,
			EventIDs:   lr.EventIDs,
			Status:     lr.Status,
			CreatedAt:  lr.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		if lr.ConfirmedBy.Valid {
			item.ConfirmedBy = lr.ConfirmedBy.String
		}
		resp = append(resp, item)
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := h.svc.Confirm(id)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"link_id": link.LinkID,
		"status":  link.Status,
	})
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := h.svc.Reject(id)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"link_id": link.LinkID,
		"status":  link.Status,
	})
}

// synonymResp is the GET /subject-synonyms(/:id) representation
// (docs/impl/v1/activation.md 步骤 3a,
// docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
type synonymResp struct {
	SynonymID   string   `json:"synonym_id"`
	DomainID    string   `json:"domain_id,omitempty"`
	Term        string   `json:"term"`
	Canonical   string   `json:"canonical"`
	Source      string   `json:"source"`
	Status      string   `json:"status"`
	CreatedFrom []string `json:"created_from,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toSynonymResp(s SubjectSynonym) synonymResp {
	r := synonymResp{
		SynonymID: s.SynonymID,
		Term:      s.Term,
		Canonical: s.Canonical,
		Source:    s.Source,
		Status:    s.Status,
		CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: s.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if s.DomainID.Valid {
		r.DomainID = s.DomainID.String
	}
	if s.CreatedFrom != "" {
		var ids []string
		if err := json.Unmarshal([]byte(s.CreatedFrom), &ids); err == nil {
			r.CreatedFrom = ids
		}
	}
	return r
}

func (h *Handler) listSynonyms(w http.ResponseWriter, r *http.Request) {
	f := ListSynonymsFilter{
		Status: r.URL.Query().Get("status"),
		Limit:  queryInt(r, "limit", 50),
		Offset: queryInt(r, "offset", 0),
	}
	rows, err := h.svc.ListSynonyms(f)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]synonymResp, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, toSynonymResp(row))
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getSynonym(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	syn, err := h.svc.GetSynonym(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if syn == nil {
		foundation.WriteError(w, http.StatusNotFound, "subject synonym not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, toSynonymResp(*syn))
}

func (h *Handler) confirmSynonym(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	syn, err := h.svc.ConfirmSynonym(id)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"synonym_id": syn.SynonymID,
		"status":     syn.Status,
	})
}

func (h *Handler) rejectSynonym(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	syn, err := h.svc.RejectSynonym(id)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"synonym_id": syn.SynonymID,
		"status":     syn.Status,
	})
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	val := r.URL.Query().Get(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}
