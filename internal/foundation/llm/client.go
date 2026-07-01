package llm

import (
	"bytes"
	"context"
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
	"github.com/jxman78/wiki-brain/internal/foundation/config"
)

type OpenAIClient struct {
	cfg        *config.LLMConfig
	httpClient *http.Client
	promptsDir string
}

func NewOpenAIClient(cfg *config.LLMConfig, promptsDir string) (*OpenAIClient, error) {
	return &OpenAIClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.TimeoutDuration()},
		promptsDir: promptsDir,
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    *float64      `json:"temperature,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	EnableThinking *bool         `json:"enable_thinking,omitempty"`
	Stream         bool          `json:"stream,omitempty"`
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
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return "", err
	}

	mc := c.cfg.ModelForPurpose(purpose)
	return c.call(ctx, prompt, mc)
}

func (c *OpenAIClient) CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	raw, err := c.Complete(ctx, promptFile, vars, model)
	if err != nil {
		return nil, err
	}

	jsonStr := c.extractAndRepairJSON(raw, promptFile)

	prompt, err := c.loadPrompt(promptFile, nil)
	if err != nil || prompt.Schema == "" {
		return []byte(jsonStr), nil
	}

	validationErr := ValidateJSONSchema(prompt.Schema, []byte(jsonStr))
	if validationErr == nil {
		return []byte(jsonStr), nil
	}

	// Schema 校验失败：尝试让模型修复缺失/错误的字段
	slog.Info("llm: schema validation failed, attempting field repair",
		"promptFile", promptFile, "error", validationErr)

	mc := c.cfg.ModelForPurpose(model)
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

// ExtractAndRepairJSON extracts a JSON object/array from raw LLM output
// (stripping markdown code fences) and repairs common syntax issues (e.g.
// unescaped newlines) via jsonrepair. Exported so callers that accumulate
// streamed chunks themselves (bypassing CompleteJSON) get the same
// robustness as the non-streaming path.
func ExtractAndRepairJSON(raw string) string {
	jsonStr := extractJSON(raw)
	if !json.Valid([]byte(jsonStr)) {
		if repaired, err := jsonrepair.Repair(jsonStr); err == nil {
			return repaired
		}
	}
	return jsonStr
}

// repairFields 针对 schema 校验失败的 JSON，让模型只修复缺失/错误的字段。
func (c *OpenAIClient) repairFields(ctx context.Context, originalJSON, validationError, schema string, mc config.ModelConfig) (string, error) {
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

	result, err := c.call(ctx, repairPrompt, mc)
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

func (c *OpenAIClient) call(ctx context.Context, prompt *Prompt, mc config.ModelConfig) (string, error) {
	var messages []chatMessage
	if prompt.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt.User})

	reqBody := chatRequest{
		Model:    mc.Model,
		Messages: messages,
	}
	temp := mc.Temperature
	reqBody.Temperature = &temp
	if mc.MaxOutputTokens > 0 {
		reqBody.MaxTokens = mc.MaxOutputTokens
	}
	thinking := mc.Thinking
	reqBody.EnableThinking = &thinking

	var lastErr error
	maxAttempts := c.cfg.MaxRetries + 1
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

		result, err := c.doRequest(ctx, reqBody)
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

func (c *OpenAIClient) doRequest(ctx context.Context, reqBody chatRequest) (string, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("llm: create request: %w", err)
	}

	apiKey := c.cfg.ResolvedAPIKey()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
	prompt, err := c.loadPrompt(promptFile, vars)
	if err != nil {
		return nil, err
	}

	mc := c.cfg.ModelForPurpose(purpose)

	var messages []chatMessage
	if prompt.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt.User})

	reqBody := chatRequest{
		Model:    mc.Model,
		Messages: messages,
		Stream:   true,
	}
	temp := mc.Temperature
	reqBody.Temperature = &temp
	if mc.MaxOutputTokens > 0 {
		reqBody.MaxTokens = mc.MaxOutputTokens
	}
	thinking := mc.Thinking
	reqBody.EnableThinking = &thinking

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("llm: create request: %w", err)
	}

	apiKey := c.cfg.ResolvedAPIKey()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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

	// 去除 ```json ... ``` 包裹
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

	// 尝试找到 JSON 对象/数组的起止
	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	open := s[start]
	var close byte = '}'
	if open == '[' {
		close = ']'
	}
	end := strings.LastIndexByte(s, close)
	if end <= start {
		return s
	}
	return s[start : end+1]
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
