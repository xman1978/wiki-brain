package wiki

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /wiki/entries/recognize", h.recognizeEntries)
	mux.HandleFunc("POST /wiki/points/filter", h.filterPoints)
	mux.HandleFunc("POST /wiki/compile/analyze", h.analyze)
	mux.HandleFunc("POST /wiki/compile", h.compile)
	mux.HandleFunc("POST /wiki/pages/{id}/publish", h.publish)
	mux.HandleFunc("POST /wiki/pages/{id}/selfcheck", h.selfcheck)
	mux.HandleFunc("POST /wiki/pages/{id}/recompile", h.recompile)
	mux.HandleFunc("POST /wiki/pages/{id}/archive", h.archive)
	mux.HandleFunc("GET /wiki/pages", h.listPages)
	mux.HandleFunc("GET /wiki/catalog", h.listCatalog)
	mux.HandleFunc("GET /wiki/pages/{id}", h.getPage)
	mux.HandleFunc("GET /wiki/pages/{id}/revisions/{rev}", h.getRevision)

	// 单层化（docs/impl/v1/wiki-single-tier-task-brief.md）: 页面关系只剩
	// related/contradicts，不再有 contains/二阶编译。
	mux.HandleFunc("GET /wiki/pages/{id}/relations", h.getRelations)
	mux.HandleFunc("POST /wiki/pages/{id}/drafts", h.createDraft)
	mux.HandleFunc("GET /wiki/drafts", h.listDrafts)
	mux.HandleFunc("GET /wiki/drafts/{id}", h.getDraft)
	mux.HandleFunc("PATCH /wiki/drafts/{id}", h.patchDraft)
	mux.HandleFunc("DELETE /wiki/drafts/{id}", h.deleteDraft)

}

type relationResp struct {
	RelationType string `json:"relation_type"`
	OtherPageID  string `json:"other_page_id"`
	Title        string `json:"title"`
	DerivedFrom  string `json:"derived_from"`
	Evidence     string `json:"evidence"`
}

func (h *Handler) getRelations(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	rels, err := h.svc.store.ListPageRelations(pageID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]relationResp, 0, len(rels))
	for _, rel := range rels {
		// related/contradicts are undirected — the "other side" is whichever
		// endpoint isn't pageID (docs/impl/v1/wiki-single-tier-task-brief.md
		// 步骤 2: contains is gone, so this no longer needs a directional case).
		other := rel.ToPageID
		if rel.FromPageID != pageID {
			other = rel.FromPageID
		}
		title := ""
		if op, err := h.svc.store.GetPage(other); err == nil && op != nil {
			title = op.Title
		}
		resp = append(resp, relationResp{
			RelationType: rel.RelationType, OtherPageID: other, Title: title,
			DerivedFrom: rel.DerivedFrom, Evidence: rel.Evidence,
		})
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

// draftResp is the snake_case JSON shape for all three draft endpoints
// (create/list/get) — the underlying Draft struct (internal/wiki/types.go)
// has no json tags at all, so serializing it directly (as listDrafts/getDraft
// used to) produces PascalCase keys (DraftID, Title, ...) the frontend's
// snake_case reads (d.draft_id, d.title, ...) silently come back undefined
// against; SourcePageIDs/EvidenceIndex are also stored as raw JSON-encoded
// strings, not arrays, so a direct pass-through would additionally hand the
// frontend a string where it expects to .map() over an array (2026-08-19 bug
// fix — surfaced by the new「编辑标题/摘要」entry point being the first thing
// to actually round-trip through listDrafts+getDraft together).
type draftResp struct {
	DraftID          string               `json:"draft_id"`
	PageID           string               `json:"page_id"`
	SourceRevisionID string               `json:"source_revision_id"`
	SourcePageIDs    []string             `json:"source_page_ids"`
	EvidenceIndex    []EvidenceIndexEntry `json:"evidence_index"`
	Title            string               `json:"title"`
	Content          string               `json:"content"`
	Note             string               `json:"note"`
	CreatedAt        string               `json:"created_at"`
	UpdatedAt        string               `json:"updated_at"`
	Stale            *bool                `json:"stale,omitempty"`
}

func toDraftResp(d Draft, stale *bool) draftResp {
	var sourcePageIDs []string
	json.Unmarshal([]byte(d.SourcePageIDs), &sourcePageIDs)
	var evidenceIndex []EvidenceIndexEntry
	json.Unmarshal([]byte(d.EvidenceIndex), &evidenceIndex)
	return draftResp{
		DraftID: d.DraftID, PageID: d.PageID, SourceRevisionID: d.SourceRevisionID,
		SourcePageIDs: nonNilStrings(sourcePageIDs), EvidenceIndex: evidenceIndex,
		Title: d.Title, Content: d.Content, Note: d.Note,
		CreatedAt: d.CreatedAt.Format(time.RFC3339), UpdatedAt: d.UpdatedAt.Format(time.RFC3339),
		Stale: stale,
	}
}

func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	var req struct {
		Mode string `json:"mode"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	draft, err := h.svc.CreateDraft(pageID, req.Mode)
	if err != nil {
		writePageError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, toDraftResp(*draft, nil))
}

func (h *Handler) listDrafts(w http.ResponseWriter, r *http.Request) {
	pageID := r.URL.Query().Get("page_id")
	limit := queryInt(r, "limit", 50)
	drafts, err := h.svc.store.ListDrafts(pageID, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]draftResp, 0, len(drafts))
	for _, d := range drafts {
		resp = append(resp, toDraftResp(d, nil))
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	draftID := r.PathValue("id")
	d, err := h.svc.GetDraftWithStale(draftID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		foundation.WriteError(w, http.StatusNotFound, "draft not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, toDraftResp(d.Draft, &d.Stale))
}

func (h *Handler) patchDraft(w http.ResponseWriter, r *http.Request) {
	draftID := r.PathValue("id")
	var req struct {
		Title   *string `json:"title"`
		Content *string `json:"content"`
		Note    *string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	d, err := h.svc.UpdateDraft(draftID, req.Title, req.Content, req.Note)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, d)
}

func (h *Handler) deleteDraft(w http.ResponseWriter, r *http.Request) {
	draftID := r.PathValue("id")
	if err := h.svc.DeleteDraft(draftID); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"draft_id": draftID, "status": "deleted"})
}

// recognizeEntries is POST /wiki/entries/recognize — the Wiki 生成弹窗的词条
// 匹配入口，复用 Retrieval 直答路径同一个 LLM 词条识别核心（Service.
// RecognizeEntries），而不是前端本地按文本重合度打分：按设计，Concept/Fact
// 词条匹配统一走模型判断（docs/impl/v1/wiki-single-tier-task-brief.md 步骤
// 4），不应该在 Wiki 生成入口另开一套纯文本启发式。
func (h *Handler) recognizeEntries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicName        string `json:"topic_name"`
		TopicDescription string `json:"topic_description"`
		DomainID         string `json:"domain_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TopicName == "" {
		foundation.WriteError(w, http.StatusBadRequest, "topic_name is required")
		return
	}

	var domainIDs []string
	if req.DomainID != "" {
		domainIDs = []string{req.DomainID}
	}
	text := req.TopicName
	if req.TopicDescription != "" {
		text += "\n" + req.TopicDescription
	}

	entryIDs, err := h.svc.RecognizeEntries(r.Context(), text, domainIDs)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{"entry_ids": nonNilStrings(entryIDs)})
}

// filterPoints is POST /wiki/points/filter — the KP-review screen in the
// Wiki 生成弹窗的模型筛选入口，与 recognizeEntries 同一批「匹配统一走模型
// 判断」的设计（用户 2026-08-19 要求：不是所有匹配到的词条下的 KP 都符合
// 主题，应该让模型按主题筛出相关的一部分，仅供页面预勾选，人工仍可调整）。
func (h *Handler) filterPoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicName        string                 `json:"topic_name"`
		TopicDescription string                 `json:"topic_description"`
		Points           []FilterPointCandidate `json:"points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TopicName == "" {
		foundation.WriteError(w, http.StatusBadRequest, "topic_name is required")
		return
	}
	if len(req.Points) == 0 {
		foundation.WriteError(w, http.StatusBadRequest, "points is required")
		return
	}

	text := req.TopicName
	if req.TopicDescription != "" {
		text += "\n" + req.TopicDescription
	}

	pointIDs, err := h.svc.FilterPoints(r.Context(), text, req.Points)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{"point_ids": nonNilStrings(pointIDs)})
}

func (h *Handler) analyze(w http.ResponseWriter, r *http.Request) {
	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.EntryIDs) == 0 {
		foundation.WriteError(w, http.StatusBadRequest, "entry_ids is required")
		return
	}

	result, err := h.svc.Analyze(r.Context(), req)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) compile(w http.ResponseWriter, r *http.Request) {
	var req CompileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.EntryIDs) == 0 {
		foundation.WriteError(w, http.StatusBadRequest, "entry_ids is required")
		return
	}

	page, err := h.svc.Compile(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrPageAlreadyExists) {
			foundation.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"page_id": page.PageID,
		"status":  page.Status,
		"title":   page.Title,
	})
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")

	var req struct {
		Force bool `json:"force"`
	}
	json.NewDecoder(r.Body).Decode(&req) // optional body; ignore decode errors (empty body is valid)

	page, err := h.svc.PublishWithForce(r.Context(), pageID, req.Force)
	if err != nil {
		writePageError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"page_id": page.PageID, "status": page.Status})
}

// selfcheck implements docs/impl/v1/wiki-generation.md 阶段 G's standalone
// entry point: POST /wiki/pages/:id/selfcheck runs the same pre-publish
// quality replay Publish gates on, without touching page status — useful for
// previewing whether a draft/needs_recompile page would clear the gate
// before attempting to publish it.
func (h *Handler) selfcheck(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	qc, err := h.svc.Selfcheck(r.Context(), pageID)
	if err != nil {
		writePageError(w, err)
		return
	}
	metrics, _ := qc.DecodeMetrics()
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"page_id":     qc.PageID,
		"revision_id": qc.RevisionID,
		"passed":      qc.Passed,
		"metrics":     metrics,
	})
}

func (h *Handler) recompile(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")

	var req struct {
		Reason       string   `json:"reason"`
		CompiledFrom []string `json:"compiled_from"`
	}
	json.NewDecoder(r.Body).Decode(&req) // optional body; ignore decode errors (empty body is valid)
	if req.Reason == "" {
		req.Reason = "manual_recompile"
	}

	page, err := h.svc.Recompile(r.Context(), pageID, req.Reason, req.CompiledFrom)
	if err != nil {
		writePageError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"page_id": page.PageID, "status": page.Status})
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	page, err := h.svc.Archive(pageID)
	if err != nil {
		writePageError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"page_id": page.PageID, "status": page.Status})
}

type pageListResp struct {
	PageID      string  `json:"page_id"`
	PageType    string  `json:"page_type"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	EntryID     string  `json:"entry_id,omitempty"`
	CompiledAt  *string `json:"compiled_at,omitempty"`
	PublishedAt *string `json:"published_at,omitempty"`
}

func toPageListResp(p Page) pageListResp {
	r := pageListResp{PageID: p.PageID, PageType: p.PageType, Title: p.Title, Status: p.Status}
	if p.EntryID.Valid {
		r.EntryID = p.EntryID.String
	}
	if p.CompiledAt.Valid {
		s := p.CompiledAt.Time.Format("2006-01-02T15:04:05Z07:00")
		r.CompiledAt = &s
	}
	if p.PublishedAt.Valid {
		s := p.PublishedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		r.PublishedAt = &s
	}
	return r
}

func (h *Handler) listPages(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	pageType := r.URL.Query().Get("page_type")
	conceptID := r.URL.Query().Get("entry_id")
	limit := queryInt(r, "limit", 50)

	pages, err := h.svc.store.ListPages(status, pageType, conceptID, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]pageListResp, 0, len(pages))
	for _, p := range pages {
		resp = append(resp, toPageListResp(p))
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

// listCatalog is GET /wiki/catalog — Wiki drawer left rail (domains) + right
// pane cards (concept/topic pages + pending wiki_candidates), grouped by
// knowledge domain. Topic pages appear under every member concept's domain.
func (h *Handler) listCatalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.svc.store.ListCatalog()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if catalog == nil {
		catalog = []CatalogDomain{}
	}
	foundation.WriteJSON(w, http.StatusOK, catalog)
}

type pageDetailResp struct {
	pageListResp
	// EntryIDs (2026-08-19 新增，用户要求标题下展示"wiki 关联的词条") is the
	// full entry set this page was compiled from (wiki_page_entries, migration
	// 057) — pageListResp.EntryID is only the primary one used for catalog's
	// domain-grouped JOIN, not every entry a multi-entry compile drew from.
	EntryIDs        []string         `json:"entry_ids"`
	Content         string           `json:"content"`
	Summary         string           `json:"summary"`
	Aspects         []PageAspect     `json:"aspects"`
	SourcePointIDs  []string         `json:"source_point_ids"`
	SourceUnitIDs   []string         `json:"source_unit_ids"`
	SourceLinkIDs   []string         `json:"source_link_ids"`
	CompiledFrom    []string         `json:"compiled_from"`
	PromptVersion   string           `json:"prompt_version"`
	ModelName       string           `json:"model_name"`
	MemberRoles     []MemberRole     `json:"member_roles"`
	UncoveredPoints []UncoveredPoint `json:"uncovered_points"`
	Revisions       []revisionMeta   `json:"revisions"`
	ClaimChecks     []claimCheckResp `json:"claim_checks"`

	// 综合满意度（2026-08-13，docs/impl/v1/wiki.md 步骤 4a / page.md 步骤 3）：
	// 只读展示字段，mean(page) 只在 SynthesisSuccessCount+SynthesisFailureCount > 0
	// 时才有意义，前端据此决定是否显示"暂无独立核实数据"。
	SynthesisSuccessCount        int     `json:"synthesis_success_count"`
	SynthesisFailureCount        int     `json:"synthesis_failure_count"`
	SynthesisAuditedSuccessCount int     `json:"synthesis_audited_success_count"`
	SynthesisAuditedFailureCount int     `json:"synthesis_audited_failure_count"`
	SynthesisMean                float64 `json:"synthesis_mean"`
}

type revisionMeta struct {
	RevisionID string `json:"revision_id"`
	Title      string `json:"title"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
}

// claimCheckResp is one wiki_claim_checks row's response shape (docs/impl/v1/
// wiki-generation.md 阶段 E) — lets the UI color-code [point_id] citations by
// whether the claim citing them was actually verified as supported by its
// material, instead of just listing aggregate verdict counts.
type claimCheckResp struct {
	ClaimID       string   `json:"claim_id"`
	ClaimText     string   `json:"claim_text"`
	CitedPointIDs []string `json:"cited_point_ids"`
	Verdict       string   `json:"verdict"`
	Reason        string   `json:"reason"`
}

func (h *Handler) getPage(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	page, err := h.svc.store.GetPage(pageID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if page == nil {
		foundation.WriteError(w, http.StatusNotFound, "page not found")
		return
	}

	entryIDs, err := h.svc.store.EntryIDsByPageID(pageID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(entryIDs) == 0 && page.EntryID.Valid && page.EntryID.String != "" {
		// Pages inserted before migration 057 (wiki_page_entries) have no rows
		// there yet — fall back to the single primary entry_id, same fallback
		// Recompile already uses.
		entryIDs = []string{page.EntryID.String}
	}

	revisions, err := h.svc.store.ListRevisions(pageID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var sourcePointIDs, sourceUnitIDs, sourceLinkIDs, compiledFrom []string
	var memberRoles []MemberRole
	var uncoveredPoints []UncoveredPoint
	var aspects []PageAspect
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	json.Unmarshal([]byte(page.SourceUnitIDs), &sourceUnitIDs)
	json.Unmarshal([]byte(page.SourceLinkIDs), &sourceLinkIDs)
	json.Unmarshal([]byte(page.CompiledFrom), &compiledFrom)
	json.Unmarshal([]byte(page.MemberRoles), &memberRoles)
	json.Unmarshal([]byte(page.UncoveredPoints), &uncoveredPoints)
	json.Unmarshal([]byte(page.Aspects), &aspects)

	revMeta := make([]revisionMeta, 0, len(revisions))
	for _, rev := range revisions {
		revMeta = append(revMeta, revisionMeta{
			RevisionID: rev.RevisionID,
			Title:      rev.Title,
			Reason:     rev.Reason,
			CreatedAt:  rev.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	// Claim checks (阶段 E 支持度核验) for the latest revision only — lets the
	// UI mark exactly which [point_id] citations in the *current* body came
	// from a claim that failed verification, without the client having to
	// separately track revision ids.
	var claimChecks []claimCheckResp
	if len(revisions) > 0 {
		latestRevisionID := revisions[len(revisions)-1].RevisionID
		checks, err := h.svc.store.ListClaimChecks(pageID, latestRevisionID)
		if err != nil {
			foundation.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		claimChecks = make([]claimCheckResp, 0, len(checks))
		for _, c := range checks {
			var cited []string
			json.Unmarshal([]byte(c.CitedPointIDs), &cited)
			claimChecks = append(claimChecks, claimCheckResp{
				ClaimID:       c.ClaimID,
				ClaimText:     c.ClaimText,
				CitedPointIDs: nonNilStrings(cited),
				Verdict:       c.Verdict,
				Reason:        c.Reason,
			})
		}
	}

	resp := pageDetailResp{
		pageListResp:    toPageListResp(*page),
		EntryIDs:        nonNilStrings(entryIDs),
		Content:         page.Content,
		Summary:         page.Summary,
		Aspects:         aspects,
		SourcePointIDs:  nonNilStrings(sourcePointIDs),
		SourceUnitIDs:   nonNilStrings(sourceUnitIDs),
		SourceLinkIDs:   nonNilStrings(sourceLinkIDs),
		CompiledFrom:    nonNilStrings(compiledFrom),
		PromptVersion:   page.PromptVersion,
		ModelName:       page.ModelName,
		MemberRoles:     memberRoles,
		UncoveredPoints: uncoveredPoints,
		Revisions:       revMeta,
		ClaimChecks:     claimChecks,

		SynthesisSuccessCount:        page.SynthesisSuccessCount,
		SynthesisFailureCount:        page.SynthesisFailureCount,
		SynthesisAuditedSuccessCount: page.SynthesisAuditedSuccessCount,
		SynthesisAuditedFailureCount: page.SynthesisAuditedFailureCount,
		SynthesisMean:                page.SynthesisMean(),
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getRevision(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	revID := r.PathValue("rev")

	rev, err := h.svc.store.GetRevision(pageID, revID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rev == nil {
		foundation.WriteError(w, http.StatusNotFound, "revision not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"revision_id": rev.RevisionID,
		"content":     rev.Content,
		"title":       rev.Title,
		"reason":      rev.Reason,
		"created_at":  rev.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func writePageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPageNotFound):
		foundation.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrPageArchived), errors.Is(err, ErrInvalidStateTransition), errors.Is(err, ErrQualityGateFailed):
		foundation.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoQualifyingPoints):
		foundation.WriteError(w, http.StatusConflict, "该词条当前没有可用于编译的合格知识点，可能是底层材料刚被 reupload 换血、新内容尚未重新积累出可信的激活记录，请补充问答让相关知识点重新收敛后再重试")
	default:
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}

func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
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
