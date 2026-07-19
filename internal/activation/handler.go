package activation

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
	mux.HandleFunc("GET /activation-links", h.list)
	mux.HandleFunc("GET /activation-links/{id}", h.get)
	mux.HandleFunc("POST /activation-links/{id}/confirm", h.confirm)
	mux.HandleFunc("POST /activation-links/{id}/reject", h.reject)
}

type linkResp struct {
	LinkID          string   `json:"link_id"`
	QuestionTerms   string   `json:"question_terms"`
	SubjectTerms    string   `json:"subject_terms,omitempty"`
	IntentTerms     []string `json:"intent_terms"`
	Audience        []string `json:"audience"`
	ConstraintTerms []string `json:"constraint_terms"`
	PointID         string   `json:"point_id"`
	PointSummary    string   `json:"point_summary,omitempty"`
	UnitCenter      string   `json:"unit_center,omitempty"`
	Status          string   `json:"status"`
	AdoptCount      int      `json:"adopt_count"`
	FailCount       int      `json:"fail_count"`
	LastUsedAt      *string  `json:"last_used_at,omitempty"`
	CreatedAt       string   `json:"created_at"`
}

func toLinkResp(l ActivationLink, pointSummary, unitCenter string) linkResp {
	r := linkResp{
		LinkID:          l.LinkID,
		QuestionTerms:   l.QuestionTerms,
		SubjectTerms:    l.SubjectTerms,
		IntentTerms:     l.IntentTerms,
		Audience:        l.Audience,
		ConstraintTerms: l.ConstraintTerms,
		PointID:         l.PointID,
		PointSummary:    pointSummary,
		UnitCenter:      unitCenter,
		Status:          l.Status,
		AdoptCount:      l.AdoptCount,
		FailCount:       l.FailCount,
		CreatedAt:       l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if l.LastUsedAt.Valid {
		s := l.LastUsedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		r.LastUsedAt = &s
	}
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f := ListLinksFilter{
		Status:  r.URL.Query().Get("status"),
		PointID: r.URL.Query().Get("point_id"),
		Limit:   queryInt(r, "limit", 50),
		Offset:  queryInt(r, "offset", 0),
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
	Scene           string           `json:"scene,omitempty"`
	Goal            string           `json:"goal,omitempty"`
	UnitID          string           `json:"unit_id,omitempty"`
	SourceTitle     string           `json:"source_title,omitempty"`
	CreatedFrom     []string         `json:"created_from"`
	LearningResults []LearningResult `json:"learning_results"`
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

	results, err := h.svc.ListLearningResults(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []LearningResult{}
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
		linkResp:        toLinkResp(*link, pointContent, unitCenter),
		Scene:           link.Scene,
		Goal:            link.Goal,
		UnitID:          unitID,
		SourceTitle:     sourceTitle,
		CreatedFrom:     createdFrom,
		LearningResults: results,
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
