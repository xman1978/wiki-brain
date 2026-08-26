package activation

import (
	"net/http"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

// Bundle management API (docs/impl/v1/activation-bundle.md 步骤 5): no
// confirm/reject queue — 阶段 1 的 auto_promote 默认 true — but the detail
// page does offer the same「清空重来」action Link has, since Bundle carries
// the identical confidence-accumulation risk (untrustworthy evidence baked
// into observed_conditions/member counts) that Link's reject endpoint exists
// to clear.
func (h *Handler) registerBundleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /activation-bundles", h.listBundles)
	mux.HandleFunc("GET /activation-bundles/{id}", h.getBundle)
	mux.HandleFunc("GET /activation-bundles/{id}/questions", h.bundleQuestions)
	mux.HandleFunc("POST /activation-bundles/{id}/reject", h.resetBundle)
}

type bundleResp struct {
	BundleID            string              `json:"bundle_id"`
	RepresentativeTerms string              `json:"representative_terms"`
	ObservedConditions  []ObservedCondition `json:"observed_conditions"`
	// Conditions mirrors linkDetailResp.Conditions — same conditionTier/
	// conditionMean computation, reused as-is so the bundle detail page can
	// show *why* a candidate bundle hasn't converged yet (each trigger-axis
	// condition's mean/tier against the same serving_confidence_min bar)
	// instead of only the raw subject/intent/audience/constraint text
	// (2026-08-17, requested after the "积累中" bundle in page.md gave no
	// visible reason).
	Conditions           []conditionResp     `json:"conditions"`
	ServingConfidenceMin float64             `json:"serving_confidence_min"`
	ConvergenceReason    string              `json:"convergence_reason,omitempty"`
	Members              []BundleMember      `json:"members"`
	MemberDetails        []bundleMemberPoint `json:"member_details"`
	CoreMemberPointIDs   []string            `json:"core_member_point_ids"`
	Status               string              `json:"status"`
	AdoptCount           int                 `json:"adopt_count"`
	FailCount            int                 `json:"fail_count"`
	LastUsedAt           *string             `json:"last_used_at,omitempty"`
	CreatedAt            string              `json:"created_at"`
}

// bundleMemberPoint 展开一个成员 point_id 背后的 KP 原文、所属 KU、所属文档
// ——详情页此前只展示 point_id UUID，用户看不出这条成员对应什么内容。
type bundleMemberPoint struct {
	PointID     string `json:"point_id"`
	Content     string `json:"content,omitempty"`
	PointType   string `json:"point_type,omitempty"`
	UnitID      string `json:"unit_id,omitempty"`
	UnitCenter  string `json:"unit_center,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`
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
	r.Conditions = toConditionResps(b.ObservedConditions, h.svc.confidenceCfg)
	r.ServingConfidenceMin = h.svc.confidenceCfg.ServingConfidenceMin
	if b.Status == StatusCandidate {
		r.ConvergenceReason = convergenceReason(b.ObservedConditions, h.svc.confidenceCfg)
	}
	r.MemberDetails = h.bundleMemberDetails(b.Members)
	return r
}

// bundleMemberDetails looks up each member point_id's KP content, owning KU,
// and owning Source in bulk so the detail page can show what a member
// actually is instead of a bare UUID. Best-effort: a point/unit/source that
// no longer resolves (deprecated lifecycle, deleted) is simply omitted from
// the enrichment fields, the point_id itself always stays present.
func (h *Handler) bundleMemberDetails(members []BundleMember) []bundleMemberPoint {
	out := make([]bundleMemberPoint, 0, len(members))
	if len(members) == 0 || h.unitStore == nil {
		return out
	}
	pointIDs := make([]string, len(members))
	for i, m := range members {
		pointIDs[i] = m.PointID
	}
	points, err := h.unitStore.GetPointsByIDs(pointIDs)
	if err != nil {
		return out
	}
	pointByID := make(map[string]unit.KnowledgePoint, len(points))
	unitIDSet := make(map[string]struct{})
	for _, p := range points {
		pointByID[p.PointID] = p
		unitIDSet[p.UnitID] = struct{}{}
	}
	unitIDs := make([]string, 0, len(unitIDSet))
	for id := range unitIDSet {
		unitIDs = append(unitIDs, id)
	}
	units, err := h.unitStore.GetUnitsByIDs(unitIDs)
	if err != nil {
		units = nil
	}
	unitByID := make(map[string]unit.KnowledgeUnit, len(units))
	sourceIDSet := make(map[string]struct{})
	for _, u := range units {
		unitByID[u.UnitID] = u
		sourceIDSet[u.SourceID] = struct{}{}
	}
	var sources []source.Source
	if h.sourceStore != nil && len(sourceIDSet) > 0 {
		sourceIDs := make([]string, 0, len(sourceIDSet))
		for id := range sourceIDSet {
			sourceIDs = append(sourceIDs, id)
		}
		if s, err := h.sourceStore.GetSourcesByIDs(sourceIDs); err == nil {
			sources = s
		}
	}
	sourceByID := make(map[string]source.Source, len(sources))
	for _, s := range sources {
		sourceByID[s.SourceID] = s
	}

	for _, m := range members {
		d := bundleMemberPoint{PointID: m.PointID}
		if p, ok := pointByID[m.PointID]; ok {
			d.Content = p.Content
			d.PointType = p.PointType
			d.UnitID = p.UnitID
			d.SourceID = p.SourceID
			if u, ok := unitByID[p.UnitID]; ok {
				d.UnitCenter = u.Center
			}
			if s, ok := sourceByID[p.SourceID]; ok {
				d.SourceTitle = s.Title
			}
		}
		out = append(out, d)
	}
	return out
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

func (h *Handler) resetBundle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := h.svc.ResetBundle(id)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"bundle_id": b.BundleID,
		"status":    b.Status,
	})
}

func (h *Handler) bundleQuestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	matched, err := h.svc.Store().ListBundleMatchedQuestions(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if matched == nil {
		matched = []LinkQuestion{}
	}
	createdFrom, err := h.svc.Store().ListBundleCreatedFromQuestions(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if createdFrom == nil {
		createdFrom = []LinkQuestion{}
	}
	foundation.WriteJSON(w, http.StatusOK, LinkQuestions{Matched: matched, CreatedFrom: createdFrom})
}
