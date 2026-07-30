package llmconfig

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /llm/platforms", h.listPlatforms)
	mux.HandleFunc("GET /llm/providers", h.listProviders)
	mux.HandleFunc("POST /llm/providers", h.createProvider)
	mux.HandleFunc("GET /llm/providers/{id}", h.getProvider)
	mux.HandleFunc("PUT /llm/providers/{id}", h.updateProvider)
	mux.HandleFunc("DELETE /llm/providers/{id}", h.deleteProvider)
	mux.HandleFunc("POST /llm/providers/{id}/test", h.testProvider)
	mux.HandleFunc("GET /llm/providers/{id}/models", h.listProviderModels)
	mux.HandleFunc("POST /llm/models/discover", h.discoverModels)
	mux.HandleFunc("GET /llm/bindings", h.getBindings)
	mux.HandleFunc("PUT /llm/bindings", h.putBindings)
}

var platformLabels = map[llm.Platform]string{
	llm.PlatformDashScope:        "阿里云百炼 (DashScope)",
	llm.PlatformDoubao:           "火山方舟 (豆包)",
	llm.PlatformZhipu:            "智谱 AI",
	llm.PlatformKimi:             "Kimi (Moonshot)",
	llm.PlatformDeepSeek:         "DeepSeek",
	llm.PlatformVLLM:             "vLLM",
	llm.PlatformOllama:           "Ollama",
	llm.PlatformOpenAICompatible: "OpenAI 兼容",
}

func (h *Handler) listPlatforms(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	items := make([]item, 0, len(llm.AllPlatforms))
	for _, p := range llm.AllPlatforms {
		items = append(items, item{ID: string(p), Label: platformLabels[p]})
	}
	foundation.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) listProviders(w http.ResponseWriter, _ *http.Request) {
	list, err := h.svc.ListProviders()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []Provider{}
	}
	foundation.WriteJSON(w, http.StatusOK, list)
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.svc.GetProvider(id)
	if errors.Is(err, ErrNotFound) {
		foundation.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var p Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	created, err := h.svc.CreateProvider(p)
	if errors.Is(err, ErrInvalidInput) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p.ProviderID = id
	updated, err := h.svc.UpdateProvider(p)
	if errors.Is(err, ErrNotFound) {
		foundation.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrInvalidInput) {
		foundation.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.svc.DeleteProvider(id)
	if errors.Is(err, ErrNotFound) {
		foundation.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, ErrInUse) {
		foundation.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	count, err := h.svc.TestProvider(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		foundation.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "model_count": count})
}

func (h *Handler) listProviderModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	models, err := h.svc.ListProviderModels(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		foundation.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		foundation.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	if models == nil {
		models = []string{}
	}
	foundation.WriteJSON(w, http.StatusOK, models)
}

func (h *Handler) discoverModels(w http.ResponseWriter, r *http.Request) {
	var body DiscoverModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	models, err := h.svc.DiscoverModels(r.Context(), body)
	if err != nil {
		foundation.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	if models == nil {
		models = []string{}
	}
	foundation.WriteJSON(w, http.StatusOK, models)
}

func (h *Handler) getBindings(w http.ResponseWriter, _ *http.Request) {
	b, err := h.svc.GetBindings()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		b = map[string]PurposeBinding{}
	}
	foundation.WriteJSON(w, http.StatusOK, b)
}

func (h *Handler) putBindings(w http.ResponseWriter, r *http.Request) {
	var body map[string]PurposeBinding
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := h.svc.SetBindings(body); err != nil {
		if errors.Is(err, ErrNotFound) {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ErrInvalidInput) {
			foundation.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusOK, body)
}
