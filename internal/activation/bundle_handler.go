package activation

import (
	"net/http"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

// Bundle read-only management API (docs/impl/v1/activation-bundle.md 步骤 5):
// no confirm/reject — 阶段 1 的 auto_promote 默认 true，没有人工确认队列.
func (h *Handler) registerBundleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /activation-bundles", h.listBundles)
	mux.HandleFunc("GET /activation-bundles/{id}", h.getBundle)
	mux.HandleFunc("GET /activation-bundles/{id}/questions", h.bundleQuestions)
}

type bundleResp struct {
	BundleID            string              `json:"bundle_id"`
	RepresentativeTerms string              `json:"representative_terms"`
	ObservedConditions  []ObservedCondition `json:"observed_conditions"`
	Members             []BundleMember      `json:"members"`
	CoreMemberPointIDs  []string            `json:"core_member_point_ids"`
	Status              string              `json:"status"`
	AdoptCount          int                 `json:"adopt_count"`
	FailCount           int                 `json:"fail_count"`
	LastUsedAt          *string             `json:"last_used_at,omitempty"`
	CreatedAt           string              `json:"created_at"`
}

func (h *Handler) toBundleResp(b ActivationBundle) bundleResp {
	r := bundleResp{
		BundleID:            b.BundleID,
		RepresentativeTerms: b.RepresentativeTerms,
		ObservedConditions:  b.ObservedConditions,
		Members:             b.Members,
		CoreMemberPointIDs:  b.CoreMemberPointIDs(h.svc.ConfidenceConfig()),
		Status:              b.Status,
		AdoptCount:          b.AdoptCount,
		FailCount:           b.FailCount,
		CreatedAt:           b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if r.ObservedConditions == nil {
		r.ObservedConditions = []ObservedCondition{}
	}
	if r.Members == nil {
		r.Members = []BundleMember{}
	}
	if r.CoreMemberPointIDs == nil {
		r.CoreMemberPointIDs = []string{}
	}
	if b.LastUsedAt.Valid {
		s := b.LastUsedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		r.LastUsedAt = &s
	}
	return r
}

func (h *Handler) listBundles(w http.ResponseWriter, r *http.Request) {
	var statuses []string
	if raw := r.URL.Query().Get("status"); raw != "" {
		statuses = strings.Split(raw, ",")
	}
	bundles, err := h.svc.Store().ListBundlesByStatus(statuses, queryInt(r, "limit", 50), queryInt(r, "offset", 0))
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]bundleResp, 0, len(bundles))
	for _, b := range bundles {
		resp = append(resp, h.toBundleResp(b))
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getBundle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := h.svc.Store().GetBundleByID(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		foundation.WriteError(w, http.StatusNotFound, "activation bundle not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, h.toBundleResp(*b))
}

func (h *Handler) bundleQuestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	qs, err := h.svc.Store().ListBundleCreatedFromQuestions(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if qs == nil {
		qs = []LinkQuestion{}
	}
	foundation.WriteJSON(w, http.StatusOK, qs)
}
