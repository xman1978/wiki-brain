package source

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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
	mux.HandleFunc("POST /sources/{id}/restore", h.restoreSource)
	mux.HandleFunc("PATCH /sources/{id}/domain", h.setSourceDomain)
mux.HandleFunc("PATCH /sources/{id}/summary", h.setSourceSummary)
	mux.HandleFunc("POST /sources/{id}/retry", h.retrySource)
	mux.HandleFunc("POST /sources/{id}/reupload", h.reuploadSource)
	mux.HandleFunc("POST /sources/{id}/reupload/retry", h.reuploadRetry)
	mux.HandleFunc("GET /sources/{id}/outlines", h.getOutlines)
	mux.HandleFunc("GET /sources/{id}/markdown", h.getMarkdown)
	mux.HandleFunc("GET /sources/{id}/preview", h.getPreview)
	mux.HandleFunc("GET /sources/{id}/progress", h.streamProgress)
	mux.HandleFunc("GET /sources/{id}/versions", h.listSourceVersions)
	mux.HandleFunc("GET /sources/{id}/versions/{version}/download", h.downloadSourceVersion)
	mux.HandleFunc("GET /sources/{id}/versions/{version}/preview", h.previewSourceVersion)
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

	// origin/origin_page_id (docs/impl/v1/wiki.md 步骤 10 "回流的自体循环必须
	// 挡住"): optional form fields, default 'upload' preserves existing
	// behavior for every caller that doesn't set them.
	origin := r.FormValue("origin")
	originPageID := r.FormValue("origin_page_id")

	src, err := h.svc.ImportWithOrigin(r.Context(), header.Filename, file, origin, originPageID)
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
		UnitsStatus         string  `json:"units_status"`
		UnitsStage          string  `json:"units_stage"`
		OutlineType         *string `json:"outline_type"`
		DomainID            *string `json:"domain_id,omitempty"`
		DomainName          *string `json:"domain_name,omitempty"`
		CreatedAt           string  `json:"created_at"`
		ProcessingStartedAt *string `json:"processing_started_at,omitempty"`
		CompletedAt         *string `json:"completed_at,omitempty"`
		UnitsCompletedAt    *string `json:"units_completed_at,omitempty"`
		UnitsBuiltAt        *string `json:"units_built_at,omitempty"`
	}

	var items []item
	for _, s := range sources {
		it := item{
			SourceID:    s.SourceID,
			Title:       s.Title,
			Format:      s.Format,
			Status:      s.Status,
			UnitsStatus: s.UnitsStatus,
			UnitsStage:  s.UnitsStage,
			CreatedAt:   s.CreatedAt.Format("2006-01-02T15:04:05Z"),
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
		if s.UnitsCompletedAt.Valid {
			t := s.UnitsCompletedAt.Time.Format("2006-01-02T15:04:05Z")
			it.UnitsCompletedAt = &t
		}
		if s.UnitsBuiltAt.Valid {
			t := s.UnitsBuiltAt.Time.Format("2006-01-02T15:04:05Z")
			it.UnitsBuiltAt = &t
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
		"source_id":    src.SourceID,
		"title":        src.Title,
		"format":       src.Format,
		"status":       src.Status,
		"units_status": src.UnitsStatus,
		"units_stage":  src.UnitsStage,
		"version":      src.Version,
		"created_at":   src.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if n, err := h.svc.store.CountManuallyEditedSemantics(id); err != nil {
		slog.Warn("get source: count manually edited semantics failed", "source_id", id, "error", err)
	} else {
		resp["manually_edited_count"] = n
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
	if src.UnitsCompletedAt.Valid {
		resp["units_completed_at"] = src.UnitsCompletedAt.Time.Format("2006-01-02T15:04:05Z")
	}
	if src.UnitsBuiltAt.Valid {
		resp["units_built_at"] = src.UnitsBuiltAt.Time.Format("2006-01-02T15:04:05Z")
	}

	foundation.WriteJSON(w, http.StatusOK, resp)
}

// deleteSource dispatches DELETE /sources/:id by current status
// (docs/impl/v1/lifecycle.md 步骤 2): failed sources — status=failed (parse
// failure) or units_status=failed (knowledge-unit extraction failure, with
// status itself completed) — are hard-deleted (unchanged MVP behavior —
// nothing useful to preserve), any other status is soft-deleted (KU/KP
// marked deprecated, rows and files kept).
func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	src, err := h.svc.Store().GetByID(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found")
		return
	}

	if src.Status == "deleted" {
		foundation.WriteError(w, http.StatusBadRequest, "source already deleted")
		return
	}

	if src.Status == "failed" || src.UnitsStatus == "failed" {
		if err := h.svc.Delete(id); err != nil {
			slog.Error("delete source failed", "error", err)
			foundation.WriteError(w, http.StatusInternalServerError, "delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	deprecated, err := h.svc.SoftDelete(id)
	if err != nil {
		slog.Error("soft delete source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id":        id,
		"deprecated_units": deprecated,
	})
}

// restoreSource implements POST /sources/:id/restore: only valid for
// soft-deleted sources (文件管理 恢复按钮). Reverses SoftDelete precisely —
// see Service.Restore.
func (h *Handler) restoreSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	src, err := h.svc.Store().GetByID(id)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found")
		return
	}
	if src.Status != "deleted" {
		foundation.WriteError(w, http.StatusBadRequest, "source is not deleted")
		return
	}

	restored, err := h.svc.Restore(id)
	if err != nil {
		slog.Error("restore source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "restore failed")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id":      id,
		"restored_units": restored,
	})
}

// setSourceDomain implements PATCH /sources/:id/domain: the file list's manual
// override for a source's knowledge domain, for when matchDomain's LLM
// classification picked the wrong one.
func (h *Handler) setSourceDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		DomainID string `json:"domain_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.svc.SetDomain(id, body.DomainID); err != nil {
		if strings.Contains(err.Error(), "source not found") {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
		if strings.Contains(err.Error(), "unknown domain_id") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("set source domain failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "set domain failed")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id": id,
		"domain_id": body.DomainID,
	})
}

// setSourceSummary implements PATCH /sources/:id/summary, letting a human
// correct the auto-generated summary that source_filter's title+summary
// pre-screen relies on.
func (h *Handler) setSourceSummary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Summary) == "" {
		foundation.WriteError(w, http.StatusBadRequest, "summary is required")
		return
	}

	if err := h.svc.SetSummary(id, body.Summary); err != nil {
		if strings.Contains(err.Error(), "source not found") {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
		slog.Error("set source summary failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "set summary failed")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id": id,
		"summary":   body.Summary,
	})
}

// reuploadSource implements POST /sources/:id/reupload (docs/impl/v1/lifecycle.md
// 步骤 2): the new file is processed through a hidden Shadow Source; the target
// itself is untouched until the shadow's unit_extract finishes successfully.
func (h *Handler) reuploadSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

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

	shadow, err := h.svc.ImportShadow(r.Context(), id, header.Filename, file)
	if err != nil {
		if strings.Contains(err.Error(), "source not found") {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
		if strings.Contains(err.Error(), "unsupported format") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "duplicate file name") {
			foundation.WriteError(w, http.StatusConflict, "文件名已存在，请先修改文件名或删除同名文件后重新上传")
			return
		}
		if strings.Contains(err.Error(), "cannot reupload a shadow") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("reupload source failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "reupload failed")
		return
	}

	foundation.WriteJSON(w, http.StatusAccepted, map[string]interface{}{
		"source_id":        id,
		"shadow_source_id": shadow.SourceID,
		"status":           "processing",
	})
}

// reuploadRetry implements POST /sources/:id/reupload/retry, resuming the
// existing failed shadow rather than starting a new one.
func (h *Handler) reuploadRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	shadow, err := h.svc.ReuploadRetry(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "no failed shadow") {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("reupload retry failed", "error", err)
		foundation.WriteError(w, http.StatusInternalServerError, "reupload retry failed")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"source_id":        id,
		"shadow_source_id": shadow.SourceID,
		"status":           "processing",
	})
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
		"source_id":    src.SourceID,
		"status":       src.Status,
		"units_status": src.UnitsStatus,
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
	unitID := r.URL.Query().Get("unit_id")
	versionStr := r.URL.Query().Get("version")

	var md string
	var err error
	switch {
	case unitID != "":
		md, err = h.svc.GetMarkdownForUnit(id, unitID)
	case versionStr != "":
		version, perr := strconv.Atoi(versionStr)
		if perr != nil {
			foundation.WriteError(w, http.StatusBadRequest, "invalid version")
			return
		}
		md, err = h.svc.GetMarkdownForVersion(id, version)
	default:
		md, err = h.svc.GetMarkdown(id)
	}
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "source not found or markdown unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	io.WriteString(w, md)
}

func (h *Handler) getPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	unitID := r.URL.Query().Get("unit_id")
	var html string
	var err error
	if unitID != "" {
		md, merr := h.svc.GetMarkdownForUnit(id, unitID)
		if merr != nil {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
		escaped := strings.ReplaceAll(md, "<", "&lt;")
		escaped = strings.ReplaceAll(escaped, ">", "&gt;")
		html = "<pre>" + escaped + "</pre>"
	} else {
		html, err = h.svc.GetHTMLPreview(id)
		if err != nil {
			foundation.WriteError(w, http.StatusNotFound, "source not found")
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, html)
}

// listSourceVersions implements GET /sources/:id/versions: the historical
// snapshots a reupload superseded (docs/impl/v1/lifecycle.md 步骤 2's archive
// step, made queryable — see migration 017_source_version.sql). Internal
// file paths are not exposed; use the download/preview endpoints for those.
func (h *Handler) listSourceVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := h.svc.store.GetSourceVersions(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type item struct {
		Version    int    `json:"version"`
		FileName   string `json:"file_name"`
		ArchivedAt string `json:"archived_at"`
	}
	items := make([]item, 0, len(versions))
	for _, v := range versions {
		items = append(items, item{
			Version:    v.Version,
			FileName:   v.FileName,
			ArchivedAt: v.ArchivedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	foundation.WriteJSON(w, http.StatusOK, items)
}

func parseVersionPathValue(r *http.Request) (int, error) {
	return strconv.Atoi(r.PathValue("version"))
}

func (h *Handler) downloadSourceVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	version, err := parseVersionPathValue(r)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid version")
		return
	}

	fullPath, fileName, err := h.svc.GetVersionOriginalPath(id, version)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "version not found")
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "archived file not found")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}

func (h *Handler) previewSourceVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	version, err := parseVersionPathValue(r)
	if err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid version")
		return
	}

	html, err := h.svc.GetVersionHTMLPreview(id, version)
	if err != nil {
		foundation.WriteError(w, http.StatusNotFound, "version not found")
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
