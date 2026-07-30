package llm

import (
	"encoding/json"
	"testing"
)

func TestMarshalChatRequest_DashScopeThinking(t *testing.T) {
	mc := ModelParams{Model: "qwen", Temperature: 0, EnableThink: true}
	body, err := marshalChatRequest(PlatformDashScope, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "")
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
	body, err := marshalChatRequest(PlatformDashScope, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "")
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
	body, err := marshalChatRequest(PlatformOllama, mc, []chatMessage{{Role: "user", Content: "hi"}}, false, false, "")
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
	cases := []struct {
		name           string
		jsonObject     bool
		responseFormat string
		wantType       string // empty => key must be absent
	}{
		{"json_mode_object", true, "json_object", "json_object"},
		{"json_mode_schema", true, "json_schema", "json_schema"},
		{"json_mode_empty_defaults", true, "", "json_object"},
		{"non_json_omits", false, "json_schema", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, tc.jsonObject, false, tc.responseFormat)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			rf, ok := m["response_format"]
			if tc.wantType == "" {
				if ok {
					t.Fatalf("response_format should be omitted, got %v", rf)
				}
				return
			}
			if !ok {
				t.Fatal("expected response_format")
			}
			obj, ok := rf.(map[string]any)
			if !ok {
				t.Fatalf("response_format type %T", rf)
			}
			if obj["type"] != tc.wantType {
				t.Fatalf("type = %v, want %s", obj["type"], tc.wantType)
			}
			if len(obj) != 1 {
				t.Fatalf("response_format must only contain type, got %v", obj)
			}
		})
	}
}
