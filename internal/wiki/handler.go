package wiki

import (
	"encoding/json"
	"errors"
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
	mux.HandleFunc("POST /wiki/compile", h.compile)
	mux.HandleFunc("POST /wiki/pages/{id}/publish", h.publish)
	mux.HandleFunc("POST /wiki/pages/{id}/recompile", h.recompile)
	mux.HandleFunc("POST /wiki/pages/{id}/archive", h.archive)
	mux.HandleFunc("GET /wiki/pages", h.listPages)
	mux.HandleFunc("GET /wiki/pages/{id}", h.getPage)
	mux.HandleFunc("GET /wiki/pages/{id}/revisions/{rev}", h.getRevision)
}

func (h *Handler) compile(w http.ResponseWriter, r *http.Request) {
	var req CompileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ConceptID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "concept_id is required")
		return
	}
	if req.PageType == "" {
		req.PageType = PageTypeConcept
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
	page, err := h.svc.Publish(pageID)
	if err != nil {
		writePageError(w, err)
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]string{"page_id": page.PageID, "status": page.Status})
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
	ConceptID   string  `json:"concept_id,omitempty"`
	CompiledAt  *string `json:"compiled_at,omitempty"`
	PublishedAt *string `json:"published_at,omitempty"`
}

func toPageListResp(p Page) pageListResp {
	r := pageListResp{PageID: p.PageID, PageType: p.PageType, Title: p.Title, Status: p.Status}
	if p.ConceptID.Valid {
		r.ConceptID = p.ConceptID.String
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
	conceptID := r.URL.Query().Get("concept_id")
	limit := queryInt(r, "limit", 50)

	pages, err := h.svc.store.ListPages(status, conceptID, limit)
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
	Content        string         `json:"content"`
	SourcePointIDs []string       `json:"source_point_ids"`
	SourceUnitIDs  []string       `json:"source_unit_ids"`
	SourceLinkIDs  []string       `json:"source_link_ids"`
	CompiledFrom   []string       `json:"compiled_from"`
	PromptVersion  string         `json:"prompt_version"`
	ModelName      string         `json:"model_name"`
	Revisions      []revisionMeta `json:"revisions"`
}

type revisionMeta struct {
	RevisionID string `json:"revision_id"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
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
	json.Unmarshal([]byte(page.SourcePointIDs), &sourcePointIDs)
	json.Unmarshal([]byte(page.SourceUnitIDs), &sourceUnitIDs)
	json.Unmarshal([]byte(page.SourceLinkIDs), &sourceLinkIDs)
	json.Unmarshal([]byte(page.CompiledFrom), &compiledFrom)

	revMeta := make([]revisionMeta, 0, len(revisions))
	for _, rev := range revisions {
		revMeta = append(revMeta, revisionMeta{
			RevisionID: rev.RevisionID,
			Reason:     rev.Reason,
			CreatedAt:  rev.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	resp := pageDetailResp{
		pageListResp:   toPageListResp(*page),
		Content:        page.Content,
		SourcePointIDs: nonNilStrings(sourcePointIDs),
		SourceUnitIDs:  nonNilStrings(sourceUnitIDs),
		SourceLinkIDs:  nonNilStrings(sourceLinkIDs),
		CompiledFrom:   nonNilStrings(compiledFrom),
		PromptVersion:  page.PromptVersion,
		ModelName:      page.ModelName,
		Revisions:      revMeta,
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
	case errors.Is(err, ErrPageArchived), errors.Is(err, ErrInvalidStateTransition):
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
