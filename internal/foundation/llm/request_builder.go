package llm

import "encoding/json"

// applyPlatformThinking merges platform-specific thinking fields into reqBody.
// reqBody is the base chat request map (model, messages, temperature, max_tokens, …).
func applyPlatformThinking(platform Platform, enableThink bool, reqBody map[string]any) {
	if !enableThink {
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
func marshalChatRequest(platform Platform, mc ModelParams, messages []chatMessage, jsonObject, stream bool, responseFormat string) ([]byte, error) {
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
		rf := responseFormat
		if rf == "" {
			rf = ResponseFormatJSONObject
		}
		reqBody["response_format"] = map[string]string{"type": rf}
	}
	if stream {
		reqBody["stream"] = true
	}
	return json.Marshal(reqBody)
}
