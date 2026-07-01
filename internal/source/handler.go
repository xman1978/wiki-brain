package source

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	mux.HandleFunc("POST /sources", h.createSource)
	mux.HandleFunc("GET /sources", h.listSources)
	mux.HandleFunc("GET /sources/{id}", h.getSource)
	mux.HandleFunc("DELETE /sources/{id}", h.deleteSource)
	mux.HandleFunc("POST /sources/{id}/retry", h.retrySource)
	mux.HandleFunc("GET /sources/{id}/outlines", h.getOutlines)
	mux.HandleFunc("GET /sources/{id}/markdown", h.getMarkdown)
	mux.HandleFunc("GET /sources/{id}/preview", h.getPreview)
	mux.HandleFunc("GET /sources/{id}/progress", h.streamProgress)
}

func (h *Handler) createSource(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	src, err := h.svc.Import(r.Context(), header.Filename, file)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported format") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "duplicate file name") {
			foundation.WriteError(w, http.StatusConflict, "文件名已存在，请先修改文件名或删除同名文件后重新上传")
			return
		}
		slog.Error("import source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "import failed")
		return
	}

	foundation.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"source_id": src.SourceID,
		"status":    src.Status,
		"title":     src.Title,
		"format":    src.Format,
	})
}

func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	domainID := r.URL.Query().Get("domain_id")
	limit := 10
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	total, err := h.svc.store.Count(status, domainID)
	if err != nil {
		slog.Error("count sources failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "count failed")
		return
	}

	sources, err := h.svc.store.List(status, domainID, limit, offset)
	if err != nil {
		slog.Error("list sources failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "list failed")
		return
	}

	domainMap := make(map[string]string)
	if domains, err := h.svc.store.ListDomains(); err == nil {
		for _, d := range domains {
			domainMap[d.DomainID] = d.Name
		}
	}

	type item struct {
		SourceID            string  `json:"source_id"`
		Title               string  `json:"title"`
		Format              string  `json:"format"`
		Status              string  `json:"status"`
		OutlineType         *string `json:"outline_type"`
		DomainID            *string `json:"domain_id,omitempty"`
		DomainName          *string `json:"domain_name,omitempty"`
		CreatedAt           string  `json:"created_at"`
		ProcessingStartedAt *string `json:"processing_started_at,omitempty"`
		CompletedAt         *string `json:"completed_at,omitempty"`
	}

	var items []item
	for _, s := range sources {
		it := item{
			SourceID:  s.SourceID,
			Title:     s.Title,
			Format:    s.Format,
			Status:    s.Status,
			CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if s.OutlineType.Valid {
			it.OutlineType = &s.OutlineType.String
		}
		if s.DomainID.Valid {
			it.DomainID = &s.DomainID.String
			if name, ok := domainMap[s.DomainID.String]; ok {
				it.DomainName = &name
			}
		}
		if s.ProcessingStartedAt.Valid {
			t := s.ProcessingStartedAt.Time.Format("2006-01-02T15:04:05Z")
			it.ProcessingStartedAt = &t
		}
		if s.CompletedAt.Valid {
			t := s.CompletedAt.Time.Format("2006-01-02T15:04:05Z")
			it.CompletedAt = &t
		}
		items = append(items, it)
	}
	if items == nil {
		items = []item{}
	}

	type domainItem struct {
		DomainID string `json:"domain_id"`
		Name     string `json:"name"`
	}
	var domainItems []domainItem
	for id, name := range domainMap {
		domainItems = append(domainItems, domainItem{DomainID: id, Name: name})
	}
	if domainItems == nil {
		domainItems = []domainItem{}
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"items":   items,
		"domains": domainItems,
	})
}

func (h *Handler) getSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	src, err := h.svc.store.GetByID(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found")
		return
	}

	resp := map[string]interface{}{
		"source_id":  src.SourceID,
		"title":      src.Title,
		"format":     src.Format,
		"status":     src.Status,
		"created_at": src.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if src.OutlineType.Valid {
		resp["outline_type"] = src.OutlineType.String
	}
	if src.Summary.Valid {
		resp["summary"] = src.Summary.String
	}
	if src.DomainID.Valid {
		resp["domain_id"] = src.DomainID.String
	}
	if src.WordCount.Valid {
		resp["word_count"] = src.WordCount.Int64
	}
	if src.ErrorMsg.Valid {
		resp["error_msg"] = src.ErrorMsg.String
	}
	if src.ProcessingStartedAt.Valid {
		resp["processing_started_at"] = src.ProcessingStartedAt.Time.Format("2006-01-02T15:04:05Z")
	}
	if src.CompletedAt.Valid {
		resp["completed_at"] = src.CompletedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	foundation.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.svc.Delete(id); err != nil {
		if strings.Contains(err.Error(), "source not found") {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
		if strings.Contains(err.Error(), "only failed sources") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("delete source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) retrySource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.svc.Retry(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not in failed state") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("retry source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "retry failed")
		return
	}

	src, _ := h.svc.store.GetByID(id)
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id": src.SourceID,
		"status":    src.Status,
	})
}

func (h *Handler) getOutlines(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tree, err := h.svc.store.GetOutlineTree(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tree)
}

func (h *Handler) getMarkdown(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	md, err := h.svc.GetMarkdown(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found or markdown unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	io.WriteString(w, md)
}

func (h *Handler) getPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	html, err := h.svc.GetHTMLPreview(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, html)
}

func (h *Handler) streamProgress(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if sourceID == "" {
		foundation.WriteError(w, http.StatusBadRequest, "missing source id")
		return
	}

	b := h.svc.Broadcaster()
	if b == nil {
		foundation.WriteError(w, http.StatusServiceUnavailable, "progress tracking not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		foundation.WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch := b.Subscribe(sourceID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: done\ndata: {\"source_id\":%q}\n\n", sourceID)
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ctx.Done():
			b.Unsubscribe(sourceID, ch)
			return
		}
	}
}
