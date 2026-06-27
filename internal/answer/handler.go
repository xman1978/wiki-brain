package answer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type Handler struct {
	svc *Service
	db  *sql.DB
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) SetDB(db *sql.DB) {
	h.db = db
}

func (h *Handler) linkAnswerToSession(answerID, sessionID string) {
	if h.db == nil || sessionID == "" {
		return
	}
	_, err := h.db.Exec(`UPDATE session_turns SET answer_id = ?
		WHERE rowid = (SELECT rowid FROM session_turns WHERE session_id = ? AND answer_id IS NULL ORDER BY turn_index DESC LIMIT 1)`,
		answerID, sessionID)
	if err != nil {
		slog.Warn("answer: failed to link answer_id to session_turns", "answer_id", answerID, "session_id", sessionID, "error", err)
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /answer", h.postAnswer)
	mux.HandleFunc("POST /answer/stream", h.postAnswerStream)
	mux.HandleFunc("GET /answers/{id}", h.getAnswer)
}

func (h *Handler) postAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question  string `json:"question"`
		Deep      bool   `json:"deep"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		foundation.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	result, err := h.svc.AnswerFromQuestion(r.Context(), req.Question, req.Deep)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.linkAnswerToSession(result.AnswerID, req.SessionID)
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) postAnswerStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question  string `json:"question"`
		Deep      bool   `json:"deep"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		foundation.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	ch, getResult, err := h.svc.AnswerStream(r.Context(), req.Question, req.Deep)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		foundation.WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	for chunk := range ch {
		switch chunk.Type {
		case llm.ChunkPhase:
			fmt.Fprintf(w, "event: phase\ndata: %s\n\n", escapeSSE(chunk.Content))
			flusher.Flush()
		case llm.ChunkThinking:
			fmt.Fprintf(w, "event: thinking\ndata: %s\n\n", escapeSSE(chunk.Content))
			flusher.Flush()
		case llm.ChunkContent:
			fmt.Fprintf(w, "event: content\ndata: %s\n\n", escapeSSE(chunk.Content))
			flusher.Flush()
		case llm.ChunkError:
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", escapeSSE(chunk.Err.Error()))
			flusher.Flush()
			return
		case llm.ChunkDone:
			result := getResult()
			if result != nil {
				h.linkAnswerToSession(result.AnswerID, req.SessionID)
				data, _ := json.Marshal(result)
				fmt.Fprintf(w, "event: result\ndata: %s\n\n", string(data))
			}
			fmt.Fprintf(w, "event: done\ndata: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}
}

func (h *Handler) getAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		foundation.WriteError(w, http.StatusBadRequest, "answer id is required")
		return
	}

	result, err := h.svc.store.Get(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result == nil {
		foundation.WriteError(w, http.StatusNotFound, "answer not found")
		return
	}

	foundation.WriteJSON(w, http.StatusOK, result)
}

func escapeSSE(s string) string {
	return strings.ReplaceAll(s, "\n", "\ndata: ")
}
