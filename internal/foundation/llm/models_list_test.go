package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOpenAIModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-3.5-turbo"}]}`))
	}))
	defer server.Close()

	rt := &ProviderRuntime{
		BaseURL:        server.URL + "/v1",
		APIKey:         "k",
		Platform:       PlatformOpenAICompatible,
		TimeoutSeconds: 30,
	}
	c, err := NewOpenAIClient(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "gpt-3.5-turbo" {
		t.Fatalf("models = %v", models)
	}
}
