package activation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

type Handler struct {
	svc         *Service
	unitStore   *unit.Store
	sourceStore *source.Store
}

// NewHandler wires unitStore/sourceStore only for read-only enrichment of
// bundle member point_ids into content/KU/Source display fields (2026-08-17)
// — activation 本身不依赖 unit/source 做任何判断逻辑，仅详情页展示用。
func NewHandler(svc *Service, unitStore *unit.Store, sourceStore *source.Store) *Handler {
	return &Handler{svc: svc, unitStore: unitStore, sourceStore: sourceStore}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /activation-links", h.list)
	mux.HandleFunc("GET /activation-links/{id}", h.get)
	mux.HandleFunc("GET /activation-links/{id}/questions", h.questions)
	mux.HandleFunc("GET /activation-links/{id}/learning-results", h.learningResults)
	mux.HandleFunc("POST /activation-links/{id}/reject", h.reject)

	mux.HandleFunc("GET /subject-synonyms", h.listSynonyms)
	mux.HandleFunc("GET /subject-synonyms/{id}", h.getSynonym)
	mux.HandleFunc("POST /subject-synonyms/{id}/confirm", h.confirmSynonym)
	mux.HandleFunc("POST /subject-synonyms/{id}/reject", h.rejectSynonym)

	h.registerBundleRoutes(mux)
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

// conditionResp is one observed condition's confidence-tier detail
// (docs/impl/v1/activation.md 步骤 3 GET /activation-links/:id — Mean/Tier
// computed at response-build time via conditionTier/conditionMean, not
// stored/cached, consistent with this codebase's "读时算" convention).
type conditionResp struct {
	Subject             string  `json:"subject"`
	Intent              string  `json:"intent"`
	Audience            string  `json:"audience"`
	Constraint          string  `json:"constraint"`
	SuccessCount        int     `json:"success_count"`
	FailureCount        int     `json:"failure_count"`
	AuditedSuccessCount int     `json:"audited_success_count"`
	AuditedFailureCount int     `json:"audited_failure_count"`
	Mean                float64 `json:"mean"`
	Tier                string  `json:"tier"`
	LastSeenAt          string  `json:"last_seen_at,omitempty"`
}

type linkDetailResp struct {
	linkResp
	Scene                string          `json:"scene,omitempty"`
	Goal                 string          `json:"goal,omitempty"`
	UnitID               string          `json:"unit_id,omitempty"`
	SourceTitle          string          `json:"source_title,omitempty"`
	CreatedFrom          []string        `json:"created_from"`
	Conditions           []conditionResp `json:"conditions"`
	ServingConfidenceMin float64         `json:"serving_confidence_min"`
	ConvergenceReason    string          `json:"convergence_reason,omitempty"`
}

// convergenceReason explains, in plain text, why a candidate link/bundle
// hasn't reached verified yet (2026-08-17, requested after the "积累中"
// status badge alone gave no visible reason). Only meaningful for
// status=candidate — verified/deprecated links don't need a "why not"
// explanation, so callers should only surface this string when status is
// candidate. Empty conditions and "conditions exist but all below
// threshold" are the only two paths deriveStatus can take to land on
// candidate (see confidence.go deriveStatus), so those are the only two
// cases explained here.
func convergenceReason(conds []ObservedCondition, cfg ConfidenceConfig) string {
	if len(conds) == 0 {
		return "尚未积累任何命中/未命中记录，还没有可计算置信度的观测条件"
	}
	var best ObservedCondition
	bestMean := -1.0
	for _, c := range conds {
		_, mean := conditionTier(c, cfg)
		if mean > bestMean {
			bestMean = mean
			best = c
		}
	}
	return fmt.Sprintf(
		"当前最高置信度条件（主体 %s）为 %.0f%%，尚未达到收敛阈值 %.0f%%（采纳 %d / 失败 %d），还需更多成功验证积累后自动收敛",
		orDash(best.Subject), bestMean*100, cfg.ServingConfidenceMin*100, best.SuccessCount, best.FailureCount,
	)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
		linkResp:             toLinkResp(*link, pointContent, unitCenter),
		Scene:                link.Scene,
		Goal:                 link.Goal,
		UnitID:               unitID,
		SourceTitle:          sourceTitle,
		CreatedFrom:          createdFrom,
		Conditions:           toConditionResps(link.ObservedConditions, h.svc.confidenceCfg),
		ServingConfidenceMin: h.svc.confidenceCfg.ServingConfidenceMin,
	}
	if link.Status == StatusCandidate {
		resp.ConvergenceReason = convergenceReason(link.ObservedConditions, h.svc.confidenceCfg)
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

func toConditionResps(conds []ObservedCondition, cfg ConfidenceConfig) []conditionResp {
	out := make([]conditionResp, 0, len(conds))
	for _, c := range conds {
		tier, mean := conditionTier(c, cfg)
		r := conditionResp{
			Subject: c.Subject, Intent: c.Intent, Audience: c.Audience, Constraint: c.Constraint,
			SuccessCount: c.SuccessCount, FailureCount: c.FailureCount,
			AuditedSuccessCount: c.AuditedSuccessCount, AuditedFailureCount: c.AuditedFailureCount,
			Mean: mean, Tier: string(tier),
		}
		if !c.LastSeenAt.IsZero() {
			r.LastSeenAt = c.LastSeenAt.Format("2006-01-02T15:04:05Z07:00")
		}
		out = append(out, r)
	}
	return out
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
