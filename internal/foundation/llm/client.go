package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaptinlin/jsonrepair"
)

type OpenAIClient struct {
	provider   *ProviderRuntime
	httpClient *http.Client
	promptsDir string
}

func NewOpenAIClient(provider *ProviderRuntime, promptsDir string) (*OpenAIClient, error) {
	if provider == nil {
		return nil, fmt.Errorf("llm: nil provider")
	}
	return &OpenAIClient{
		provider:   provider,
		httpClient: &http.Client{Timeout: provider.TimeoutDuration()},
		promptsDir: promptsDir,
	}, nil
}

type chatMessage struct {
	Role string `json:"role"`
	// Content is a plain string for text-only messages, or []chatContentPart
	// for multimodal messages built by buildImageMessage. encoding/json
	// marshals either shape correctly without a custom MarshalJSON.
	Content any `json:"content"`
}

// chatContentPart is one element of an OpenAI-compatible multimodal
// message's content array.
type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

func buildImageMessage(role, text string, images []ImageInput) chatMessage {
	parts := make([]chatContentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, chatContentPart{Type: "text", Text: text})
	}
	for _, img := range images {
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
		}
		dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: dataURI}})
	}
	return chatMessage{Role: role, Content: parts}
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			ReasoningContent string `json:"reasoning_content"`
			Content          string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (c *OpenAIClient) Complete(ctx context.Context, promptFile string, vars map[string]string, purpose string) (string, error) {
	mc := c.provider.ModelForPurpose(purpose)
	return c.CompleteWithParams(ctx, promptFile, vars, mc)
}

func (c *OpenAIClient) CompleteWithParams(ctx context.Context, promptFile string, vars map[string]string, mc ModelParams) (string, error) {
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return "", err
	}
	return c.call(ctx, prompt, mc, false)
}

func (c *OpenAIClient) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, purpose string) ([]byte, error) {
	mc := c.provider.ModelForPurpose(purpose)
	return c.CompleteJSONWithParams(ctx, promptFile, vars, mc)
}

func (c *OpenAIClient) CompleteJSONWithParams(ctx context.Context, promptFile string, vars map[string]string, mc ModelParams) ([]byte, error) {
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return nil, err
	}

	raw, err := c.call(ctx, prompt, mc, true)
	if err != nil {
		return nil, err
	}
	jsonStr := c.extractAndRepairJSON(raw, promptFile)

	prompt, err = c.loadPrompt(promptFile, nil)
	if err != nil || prompt.Schema == "" {
		return []byte(jsonStr), nil
	}

	validationErr := ValidateJSONSchema(prompt.Schema, []byte(jsonStr))
	if validationErr == nil {
		return []byte(jsonStr), nil
	}

	slog.Info("llm: schema validation failed, attempting field repair",
		"promptFile", promptFile, "error", validationErr)

	repaired, err := c.repairFields(ctx, jsonStr, validationErr.Error(), prompt.Schema, mc)
	if err != nil {
		slog.Warn("llm: field repair failed, returning original validation error",
			"promptFile", promptFile, "repairError", err)
		return nil, validationErr
	}

	if err := ValidateJSONSchema(prompt.Schema, []byte(repaired)); err != nil {
		slog.Warn("llm: repaired JSON still fails schema validation",
			"promptFile", promptFile, "error", err)
		return nil, fmt.Errorf("%w: repair attempted but still invalid: %v", ErrSchemaValidation, err)
	}

	slog.Info("llm: field repair succeeded", "promptFile", promptFile)
	return []byte(repaired), nil
}

func (c *OpenAIClient) extractAndRepairJSON(raw, promptFile string) string {
	before := extractJSON(raw)
	after := ExtractAndRepairJSON(raw)
	if after != before {
		slog.Debug("llm: repaired JSON syntax", "promptFile", promptFile)
	}
	return after
}

func ExtractAndRepairJSON(raw string) string {
	jsonStr := extractJSON(raw)
	if !json.Valid([]byte(jsonStr)) {
		if repaired, err := jsonrepair.Repair(jsonStr); err == nil {
			return repaired
		}
	}
	return jsonStr
}

func (c *OpenAIClient) repairFields(ctx context.Context, originalJSON, validationError, schema string, mc ModelParams) (string, error) {
	repairPrompt := &Prompt{
		System: `你是 JSON 修复助手。用户会给你一段 JSON 和校验错误信息。
请只修复错误提到的字段（补全缺失字段、修正类型错误的值），保持其他字段不变。
直接输出修复后的完整 JSON，不输出任何其他文字。`,
		User: fmt.Sprintf(`以下 JSON 未通过 schema 校验：

原始 JSON：
%s

校验错误：
%s

JSON Schema 要求：
%s

请修复上述错误，只改动有问题的字段，输出修复后的完整 JSON。`, originalJSON, validationError, schema),
	}

	result, err := c.call(ctx, repairPrompt, mc, true)
	if err != nil {
		return "", fmt.Errorf("repair call failed: %w", err)
	}

	repaired := c.extractAndRepairJSON(result, "repair")
	if !json.Valid([]byte(repaired)) {
		return "", fmt.Errorf("repair output is not valid JSON")
	}

	return repaired, nil
}

func (c *OpenAIClient) loadPrompt(promptFile string, vars map[string]string) (*Prompt, error) {
	path := filepath.Join(c.promptsDir, promptFile)
	return LoadPrompt(path, vars)
}

func (c *OpenAIClient) call(ctx context.Context, prompt *Prompt, mc ModelParams, jsonObject bool) (string, error) {
	var messages []chatMessage
	if prompt.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt.User})
	return c.callMessages(ctx, messages, mc, jsonObject, prompt.Schema)
}

// CompleteImage is the LLMClient.CompleteImage implementation: the prompt's
// User section plus the given images are sent as one multipart user message.
func (c *OpenAIClient) CompleteImage(ctx context.Context, promptFile string, vars map[string]string, images []ImageInput, purpose string) (string, error) {
	mc := c.provider.ModelForPurpose(purpose)
	return c.CompleteImageWithParams(ctx, promptFile, vars, images, mc)
}

func (c *OpenAIClient) CompleteImageWithParams(ctx context.Context, promptFile string, vars map[string]string, images []ImageInput, mc ModelParams) (string, error) {
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return "", err
	}
	var messages []chatMessage
	if prompt.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, buildImageMessage("user", prompt.User, images))
	return c.callMessages(ctx, messages, mc, false, "")
}

func (c *OpenAIClient) callMessages(ctx context.Context, messages []chatMessage, mc ModelParams, jsonObject bool, schemaJSON string) (string, error) {
	bodyBytes, err := marshalChatRequest(c.provider.Platform, mc, messages, jsonObject, false, c.provider.ResponseFormat, schemaJSON)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	var lastErr error
	maxAttempts := c.provider.MaxRetries + 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			slog.Info("llm retry", "attempt", attempt+1, "backoff", backoff)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}

		result, err := c.doRequest(ctx, bodyBytes)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	return "", lastErr
}

func (c *OpenAIClient) doRequest(ctx context.Context, bodyBytes []byte) (string, error) {
	url := strings.TrimRight(c.provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("llm: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.provider.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) || strings.Contains(err.Error(), "deadline exceeded") {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("llm: non-200 response", "status", resp.StatusCode, "body", truncate(string(respBytes), 500))
		return "", fmt.Errorf("%w: status %d: %s", ErrModelError, resp.StatusCode, truncate(string(respBytes), 200))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("%w: %s", ErrModelError, chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("%w: empty choices", ErrModelError)
	}

	content := stripThinkTags(chatResp.Choices[0].Message.Content)
	return content, nil
}

type streamDelta struct {
	Choices []struct {
		Delta struct {
			ReasoningContent string `json:"reasoning_content"`
			Content          string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAIClient) CompleteStream(ctx context.Context, promptFile string, vars map[string]string, purpose string) (<-chan StreamChunk, error) {
	mc := c.provider.ModelForPurpose(purpose)
	return c.CompleteStreamWithParams(ctx, promptFile, vars, mc)
}

func (c *OpenAIClient) CompleteStreamWithParams(ctx context.Context, promptFile string, vars map[string]string, mc ModelParams) (<-chan StreamChunk, error) {
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return nil, err
	}

	var messages []chatMessage
	if prompt.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt.User})

	bodyBytes, err := marshalChatRequest(c.provider.Platform, mc, messages, true, true, c.provider.ResponseFormat, prompt.Schema)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := strings.TrimRight(c.provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("llm: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.provider.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) || strings.Contains(err.Error(), "deadline exceeded") {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("llm: request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("%w: status %d: %s", ErrModelError, resp.StatusCode, truncate(string(body), 200))
	}

	ch := make(chan StreamChunk, 32)
	go c.readSSE(resp, ch)
	return ch, nil
}

func (c *OpenAIClient) readSSE(resp *http.Response, ch chan<- StreamChunk) {
	defer resp.Body.Close()
	defer close(ch)

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	inThinking := false

	for {
		n, readErr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)

			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx == -1 {
					break
				}
				line := string(buf[:idx])
				buf = buf[idx+1:]

				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					ch <- StreamChunk{Type: ChunkDone}
					return
				}

				var delta streamDelta
				if err := json.Unmarshal([]byte(data), &delta); err != nil {
					continue
				}

				if delta.Error != nil {
					ch <- StreamChunk{Type: ChunkError, Err: fmt.Errorf("%w: %s", ErrModelError, delta.Error.Message)}
					return
				}

				if len(delta.Choices) == 0 {
					continue
				}

				choice := delta.Choices[0]

				if choice.Delta.ReasoningContent != "" {
					if !inThinking {
						inThinking = true
					}
					ch <- StreamChunk{Type: ChunkThinking, Content: choice.Delta.ReasoningContent}
				}

				if choice.Delta.Content != "" {
					if inThinking {
						inThinking = false
					}
					ch <- StreamChunk{Type: ChunkContent, Content: choice.Delta.Content}
				}

				if choice.FinishReason != nil {
					ch <- StreamChunk{Type: ChunkDone}
					return
				}
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				ch <- StreamChunk{Type: ChunkError, Err: readErr}
			} else {
				ch <- StreamChunk{Type: ChunkDone}
			}
			return
		}
	}
}

func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(s, "</think>")
		if end == -1 {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)

	if strings.HasPrefix(s, "```") {
		firstNewline := strings.Index(s, "\n")
		if firstNewline == -1 {
			return s
		}
		rest := s[firstNewline+1:]
		end := strings.LastIndex(rest, "```")
		if end != -1 {
			return strings.TrimSpace(rest[:end])
		}
		return strings.TrimSpace(rest)
	}

	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	open := s[start]
	var close byte = '}'
	if open == '[' {
		close = ']'
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// TestPing sends a minimal completion to verify connectivity.
func (c *OpenAIClient) TestPing(ctx context.Context) error {
	mc := c.provider.ModelForPurpose("default")
	return c.TestPingWithParams(ctx, mc)
}

func (c *OpenAIClient) TestPingWithParams(ctx context.Context, mc ModelParams) error {
	if mc.Model == "" {
		return fmt.Errorf("model not configured")
	}
	prompt := &Prompt{User: "ping"}
	_, err := c.call(ctx, prompt, mc, false)
	return err
}
