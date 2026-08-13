package wiki

import (
	"encoding/json"
	"errors"
	"log/slog"
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
	mux.HandleFunc("POST /wiki/compile/analyze", h.analyze)
	mux.HandleFunc("POST /wiki/compile", h.compile)
	mux.HandleFunc("POST /wiki/pages/{id}/publish", h.publish)
	mux.HandleFunc("POST /wiki/pages/{id}/selfcheck", h.selfcheck)
	mux.HandleFunc("POST /wiki/pages/{id}/recompile", h.recompile)
	mux.HandleFunc("POST /wiki/pages/{id}/archive", h.archive)
	mux.HandleFunc("GET /wiki/pages", h.listPages)
	mux.HandleFunc("GET /wiki/catalog", h.listCatalog)
	mux.HandleFunc("GET /wiki/topics", h.listTopicPages)
	mux.HandleFunc("POST /wiki/topics", h.createTopic)
	mux.HandleFunc("POST /wiki/topics/candidates", h.previewTopicCandidates)
	mux.HandleFunc("POST /wiki/topics/draft", h.createTopicDraft)
	mux.HandleFunc("POST /wiki/wizard/tasks", h.startWizardTask)
	mux.HandleFunc("GET /wiki/wizard/tasks/{id}", h.getWizardTask)
	mux.HandleFunc("PATCH /wiki/wizard/tasks/{id}", h.patchWizardTask)
	mux.HandleFunc("DELETE /wiki/wizard/tasks/{id}", h.deleteWizardTask)
	mux.HandleFunc("GET /wiki/topics/{id}/members", h.listTopicMembers)
	mux.HandleFunc("GET /wiki/unassigned-entries", h.listUnassignedEntryPages)
	mux.HandleFunc("GET /wiki/pages/{id}", h.getPage)
	mux.HandleFunc("GET /wiki/pages/{id}/revisions/{rev}", h.getRevision)

	// 两层架构扩展（步骤 7-10）
	mux.HandleFunc("GET /wiki/pages/{id}/relations", h.getRelations)
	mux.HandleFunc("POST /wiki/pages/{id}/topic/analyze", h.topicAnalyze)
	mux.HandleFunc("POST /wiki/pages/{id}/topic/compile", h.topicCompile)
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
		other := rel.ToPageID
		if rel.RelationType != RelationContains && rel.FromPageID != pageID {
			other = rel.FromPageID
		} else if rel.RelationType == RelationContains && rel.ToPageID == pageID {
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

func (h *Handler) topicAnalyze(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	result, err := h.svc.AnalyzeTopic(r.Context(), pageID)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) topicCompile(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	var req struct {
		Claims   []Claim   `json:"claims"`
		Tensions []Tension `json:"tensions"`
	}
	json.NewDecoder(r.Body).Decode(&req) // optional body

	page, err := h.svc.CompileTopic(r.Context(), pageID, req.Claims, req.Tensions)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{
		"page_id": page.PageID, "status": page.Status, "title": page.Title,
	})
}

func writeTopicError(w http.ResponseWriter, err error) {
	var notPublished *ErrMembersNotPublished
	if errors.As(err, &notPublished) {
		foundation.WriteJSON(w, http.StatusConflict, map[string]interface{}{
			"error": err.Error(), "pending": notPublished.Pending,
		})
		return
	}
	var badMembers *ErrInvalidTopicMembers
	if errors.As(err, &badMembers) {
		foundation.WriteError(w, http.StatusBadRequest, badMembers.Message)
		return
	}
	writePageError(w, err)
}

// previewTopicCandidates is POST /wiki/topics/candidates — docs/impl/v1/wiki.md
// 步骤 8 "分步向导" 步骤 1: read-only preview of the same candidate-range
// retrieval + qualifying + grouping CreateTopicManual uses, but with no side
// effects, so a human can see per-entry readiness before choosing which
// unready entries to force-compile via the existing POST /wiki/compile.
func (h *Handler) previewTopicCandidates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicName        string `json:"topic_name"`
		TopicDescription string `json:"topic_description"`
		DomainID         string `json:"domain_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entries, err := h.svc.PreviewTopicCandidates(r.Context(), req.TopicName, req.TopicDescription, req.DomainID)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// createTopicDraft is POST /wiki/topics/draft — docs/impl/v1/wiki.md 步骤 8
// "分步向导" 步骤 3: build a draft topic shell from an explicit, human-picked
// member_page_ids list (unlike createTopic/CreateTopicManual, membership is
// not computed from isEntryReady).
func (h *Handler) createTopicDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicName     string   `json:"topic_name"`
		MemberPageIDs []string `json:"member_page_ids"`
		TaskID        string   `json:"task_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cand, err := h.svc.CreateTopicFromMembers(req.TopicName, req.MemberPageIDs)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	// 分步向导提交成功即释放该领域的向导任务名额（docs/impl/v1/wiki.md
	// 步骤 8 "分步向导" 断点续开）——任务完成后没有继续存在的意义，不设
	// completed 状态，直接删除。
	if req.TaskID != "" {
		if err := h.svc.DeleteWizardTask(req.TaskID); err != nil {
			slog.Warn("wiki: delete wizard task after draft creation failed", "task_id", req.TaskID, "error", err)
		}
	}
	title := ""
	if page, err := h.svc.store.GetPage(cand.PageID); err == nil && page != nil {
		title = page.Title
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"page_id":         cand.PageID,
		"status":          StatusDraft,
		"title":           title,
		"member_page_ids": cand.MemberPageIDs,
	})
}

// wizard task routes (docs/impl/v1/wiki.md 步骤 8 "分步向导" 断点续开,
// 2026-08-07 新增): persist step-1 candidate retrieval progress so it
// survives a page reload / accidental modal close.

func (h *Handler) startWizardTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID         string `json:"domain_id"`
		TopicName        string `json:"topic_name"`
		TopicDescription string `json:"topic_description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	task, err := h.svc.StartWizardTask(req.TopicName, req.TopicDescription, req.DomainID)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, wizardTaskResp(task))
}

func (h *Handler) getWizardTask(w http.ResponseWriter, r *http.Request) {
	detail, err := h.svc.GetWizardTaskDetail(r.PathValue("id"))
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if detail == nil {
		foundation.WriteError(w, http.StatusNotFound, "wizard task not found")
		return
	}
	foundation.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) patchWizardTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelectedMembers []string `json:"selected_members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.UpdateWizardTaskSelectedMembers(r.PathValue("id"), req.SelectedMembers); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) deleteWizardTask(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteWizardTask(r.PathValue("id")); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func wizardTaskResp(t *WizardTask) map[string]interface{} {
	return map[string]interface{}{
		"task_id": t.TaskID, "domain_id": t.DomainID, "topic_name": t.TopicName,
		"topic_description": t.TopicDescription, "status": t.Status,
	}
}

// createTopic is POST /wiki/topics — docs/impl/v1/wiki.md 步骤 8
// "人工手动指定主题" (2026-08-03 修订): the request gives a topic *scope*
// (name/description[/domain]), not a member-page list. Builds a draft shell
// + contains for already-published qualifying entries; the caller then
// drives topic/analyze → topic/compile.
func (h *Handler) createTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicName        string `json:"topic_name"`
		TopicDescription string `json:"topic_description"`
		DomainID         string `json:"domain_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cand, readiness, err := h.svc.CreateTopicManual(r.Context(), req.TopicName, req.TopicDescription, req.DomainID)
	if err != nil {
		writeTopicError(w, err)
		return
	}
	title := ""
	if page, err := h.svc.store.GetPage(cand.PageID); err == nil && page != nil {
		title = page.Title
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"page_id":           cand.PageID,
		"status":            StatusDraft,
		"title":             title,
		"member_page_ids":   cand.MemberPageIDs,
		"pending_concepts":  cand.PendingEntries,
		"uncovered_entries": cand.UncoveredEntries,
		"readiness":         readiness,
	})
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
	var sourcePageIDs []string
	json.Unmarshal([]byte(draft.SourcePageIDs), &sourcePageIDs)
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"draft_id": draft.DraftID, "page_id": draft.PageID, "mode": req.Mode,
		"source_page_ids": nonNilStrings(sourcePageIDs), "title": draft.Title,
	})
}

func (h *Handler) listDrafts(w http.ResponseWriter, r *http.Request) {
	pageID := r.URL.Query().Get("page_id")
	limit := queryInt(r, "limit", 50)
	drafts, err := h.svc.store.ListDrafts(pageID, limit)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, drafts)
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
	foundation.WriteJSON(w, http.StatusOK, d)
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
	if err := h.svc.store.DeleteDraft(draftID); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"draft_id": draftID, "status": "deleted"})
}

func (h *Handler) analyze(w http.ResponseWriter, r *http.Request) {
	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EntryID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "entry_id is required")
		return
	}
	// page_type left empty is fine — Service.Analyze derives it from the
	// concept's kind (docs/impl/v1/wiki.md「概念页 / 事实页」); only an
	// explicit, wrong value (e.g. "topic") is rejected there.

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
	if req.EntryID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "entry_id is required")
		return
	}
	// page_type left empty is fine — Service.Compile derives it from the
	// concept's kind (docs/impl/v1/wiki.md「概念页 / 事实页」); only an
	// explicit, wrong value (e.g. "topic") is rejected there.

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

// topicPageResp is a topic page plus its live member count, for the 知识地图
// page's left rail.
type topicPageResp struct {
	pageListResp
	MemberCount int `json:"member_count"`
}

func (h *Handler) listTopicPages(w http.ResponseWriter, r *http.Request) {
	topics, err := h.svc.store.ListTopicPages()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]topicPageResp, 0, len(topics))
	for _, t := range topics {
		resp = append(resp, topicPageResp{pageListResp: toPageListResp(t.Page), MemberCount: t.MemberCount})
	}
	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) listTopicMembers(w http.ResponseWriter, r *http.Request) {
	topicID := r.PathValue("id")
	pages, err := h.svc.store.ListTopicMemberPages(topicID)
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

func (h *Handler) listUnassignedEntryPages(w http.ResponseWriter, r *http.Request) {
	pages, err := h.svc.store.ListUnassignedEntryPages()
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

type pageDetailResp struct {
	pageListResp
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
