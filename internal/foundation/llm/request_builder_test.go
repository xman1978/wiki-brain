package llm

import (
	"encoding/json"
	"testing"
)

func TestMarshalChatRequest_DashScopeThinking(t *testing.T) {
	mc := ModelParams{Model: "qwen", Temperature: 0, EnableThink: true}
	body, err := marshalChatRequest(PlatformDashScope, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m["enable_thinking"] != true {
		t.Fatalf("expected enable_thinking true, got %v", m["enable_thinking"])
	}
}

func TestMarshalChatRequest_ThinkingOffOmits(t *testing.T) {
	mc := ModelParams{Model: "qwen", EnableThink: false}
	body, err := marshalChatRequest(PlatformDashScope, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(body, &m)
	if _, ok := m["enable_thinking"]; ok {
		t.Fatal("enable_thinking should be omitted when false")
	}
}

func TestMarshalChatRequest_OllamaThink(t *testing.T) {
	mc := ModelParams{Model: "llama", EnableThink: true}
	body, err := marshalChatRequest(PlatformOllama, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(body, &m)
	if m["think"] != true {
		t.Fatalf("expected think true, got %v", m["think"])
	}
}

func TestMarshalChatRequest_ResponseFormat(t *testing.T) {
	mc := ModelParams{Model: "m", Temperature: 0}
	msgs := []chatMessage{{Role: "user", Content: "hi"}}
	schema := `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`

	t.Run("json_mode_object", func(t *testing.T) {
		body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, true, false, ResponseFormatJSONObject, schema)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		obj := m["response_format"].(map[string]any)
		if obj["type"] != ResponseFormatJSONObject {
			t.Fatalf("type = %v", obj["type"])
		}
		if _, ok := obj["json_schema"]; ok {
			t.Fatal("json_object must not include json_schema payload")
		}
		if len(obj) != 1 {
			t.Fatalf("want only type, got %v", obj)
		}
	})

	t.Run("json_mode_schema_with_object", func(t *testing.T) {
		body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, true, false, ResponseFormatJSONSchema, schema)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		obj := m["response_format"].(map[string]any)
		if obj["type"] != ResponseFormatJSONSchema {
			t.Fatalf("type = %v", obj["type"])
		}
		js, ok := obj["json_schema"].(map[string]any)
		if !ok {
			t.Fatalf("json_schema missing: %v", obj)
		}
		if js["name"] != "response" {
			t.Fatalf("name = %v", js["name"])
		}
		if js["strict"] != true {
			t.Fatalf("strict = %v", js["strict"])
		}
		sch, ok := js["schema"].(map[string]any)
		if !ok || sch["type"] != "object" {
			t.Fatalf("schema = %v", js["schema"])
		}
	})

	t.Run("json_mode_schema_empty_uses_object_fallback", func(t *testing.T) {
		body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, true, false, ResponseFormatJSONSchema, "")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(body, &m)
		js := m["response_format"].(map[string]any)["json_schema"].(map[string]any)
		sch := js["schema"].(map[string]any)
		if sch["type"] != "object" {
			t.Fatalf("fallback schema = %v", sch)
		}
	})

	t.Run("json_mode_empty_defaults_object", func(t *testing.T) {
		body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, true, false, "", "")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(body, &m)
		obj := m["response_format"].(map[string]any)
		if obj["type"] != ResponseFormatJSONObject || len(obj) != 1 {
			t.Fatalf("got %v", obj)
		}
	})

	t.Run("non_json_omits", func(t *testing.T) {
		body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, false, false, ResponseFormatJSONSchema, schema)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(body, &m)
		if _, ok := m["response_format"]; ok {
			t.Fatal("response_format should be omitted")
		}
	})
}
