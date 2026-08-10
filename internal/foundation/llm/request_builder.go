package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// applyPlatformThinking merges platform-specific thinking fields into reqBody.
// reqBody is the base chat request map (model, messages, temperature, max_tokens, …).
//
// DeepSeek / Doubao default thinking on; enable_think=false must send
// thinking.type=disabled rather than omitting the field.
// DashScope (Qwen hybrid) likewise defaults thinking on for many models;
// enable_think=false must send enable_thinking=false rather than omitting.
func applyPlatformThinking(platform Platform, enableThink bool, reqBody map[string]any) {
	if !enableThink {
		switch platform {
		case PlatformDoubao, PlatformDeepSeek:
			reqBody["thinking"] = map[string]any{"type": "disabled"}
		case PlatformDashScope:
			reqBody["enable_thinking"] = false
		}
		return
	}
	switch platform {
	case PlatformOllama:
		reqBody["think"] = true
	case PlatformDoubao, PlatformZhipu, PlatformDeepSeek:
		reqBody["thinking"] = map[string]any{"type": "enabled"}
	default:
		// dashscope, kimi, vllm, openai_compatible
		reqBody["enable_thinking"] = true
	}
}

// marshalChatRequest builds the JSON body for POST /chat/completions.
// schemaJSON is the prompt ## Schema body; used only when responseFormat is json_schema.
func marshalChatRequest(platform Platform, mc ModelParams, messages []chatMessage, jsonObject, stream bool, responseFormat, schemaJSON string) ([]byte, error) {
	reqBody := map[string]any{
		"model":    mc.Model,
		"messages": messages,
	}
	reqBody["temperature"] = mc.Temperature
	if mc.MaxOutputTokens > 0 {
		reqBody["max_tokens"] = mc.MaxOutputTokens
	}
	applyPlatformThinking(platform, mc.EnableThink, reqBody)
	if jsonObject {
		rf, err := buildResponseFormat(responseFormat, schemaJSON)
		if err != nil {
			return nil, err
		}
		reqBody["response_format"] = rf
	}
	if stream {
		reqBody["stream"] = true
	}
	return json.Marshal(reqBody)
}

func buildResponseFormat(responseFormat, schemaJSON string) (map[string]any, error) {
	rf := responseFormat
	if rf == "" {
		rf = ResponseFormatJSONObject
	}
	switch rf {
	case ResponseFormatJSONObject:
		return map[string]any{"type": ResponseFormatJSONObject}, nil
	case ResponseFormatJSONSchema:
		schema, err := parseSchemaOrFallback(schemaJSON)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type": ResponseFormatJSONSchema,
			"json_schema": map[string]any{
				"name":   "response",
				"strict": true,
				"schema": schema,
			},
		}, nil
	default:
		return map[string]any{"type": rf}, nil
	}
}

func parseSchemaOrFallback(schemaJSON string) (any, error) {
	if strings.TrimSpace(schemaJSON) == "" {
		return map[string]any{"type": "object"}, nil
	}
	var schema any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return nil, fmt.Errorf("llm: invalid prompt schema JSON: %w", err)
	}
	return schema, nil
}
