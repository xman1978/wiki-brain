package llmconfig

import (
	"errors"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func validProvider() Provider {
	return Provider{
		Name:           "t",
		Platform:       llm.PlatformOpenAICompatible,
		BaseURL:        "http://localhost/v1",
		TimeoutSeconds: 120,
	}
}

func TestValidateProvider_ResponseFormat(t *testing.T) {
	p := validProvider()
	p.ResponseFormat = ""
	if err := ValidateProvider(&p); err != nil {
		t.Fatal(err)
	}
	if p.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("got %q", p.ResponseFormat)
	}

	p = validProvider()
	p.ResponseFormat = llm.ResponseFormatJSONSchema
	if err := ValidateProvider(&p); err != nil {
		t.Fatal(err)
	}

	p = validProvider()
	p.ResponseFormat = "text"
	if err := ValidateProvider(&p); err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestProviderToRuntime_ResponseFormat(t *testing.T) {
	p := validProvider()
	p.ProviderID = "id1"
	p.ResponseFormat = llm.ResponseFormatJSONSchema
	rt := ProviderToRuntime(p)
	if rt.ResponseFormat != llm.ResponseFormatJSONSchema {
		t.Fatalf("ResponseFormat = %q", rt.ResponseFormat)
	}
}
