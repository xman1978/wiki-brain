package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type FakeResponse struct {
	Output string
	Err    error
}

type FakeClient struct {
	mu        sync.Mutex
	responses map[string]FakeResponse
	calls     []FakeCall
}

type FakeCall struct {
	PromptFile string
	Vars       map[string]string
	Model      string
}

func NewFakeClient() *FakeClient {
	return &FakeClient{
		responses: make(map[string]FakeResponse),
	}
}

func (f *FakeClient) SetResponse(promptFile string, resp FakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[promptFile] = resp
}

func (f *FakeClient) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *FakeClient) recordCall(promptFile, model string, vars map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{
		PromptFile: promptFile,
		Vars:       vars,
		Model:      model,
	})
}

func (f *FakeClient) Complete(_ context.Context, promptFile string, vars map[string]string, model string) (string, error) {
	f.recordCall(promptFile, model, vars)

	f.mu.Lock()
	resp, ok := f.responses[promptFile]
	f.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("fake: no response configured for %q", promptFile)
	}
	return resp.Output, resp.Err
}

func (f *FakeClient) CompleteJSON(_ context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	f.recordCall(promptFile, model, vars)

	f.mu.Lock()
	resp, ok := f.responses[promptFile]
	f.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("fake: no response configured for %q", promptFile)
	}
	if resp.Err != nil {
		return nil, resp.Err
	}

	if !json.Valid([]byte(resp.Output)) {
		return nil, fmt.Errorf("%w: output is not valid JSON", ErrSchemaValidation)
	}

	return []byte(resp.Output), nil
}

func (f *FakeClient) CompleteStream(_ context.Context, promptFile string, vars map[string]string, model string) (<-chan StreamChunk, error) {
	f.recordCall(promptFile, model, vars)

	f.mu.Lock()
	resp, ok := f.responses[promptFile]
	f.mu.Unlock()

	ch := make(chan StreamChunk, 8)
	if !ok {
		go func() {
			ch <- StreamChunk{Type: ChunkError, Err: fmt.Errorf("fake: no response configured for %q", promptFile)}
			close(ch)
		}()
		return ch, nil
	}
	if resp.Err != nil {
		return nil, resp.Err
	}

	go func() {
		for _, r := range resp.Output {
			ch <- StreamChunk{Type: ChunkContent, Content: string(r)}
		}
		ch <- StreamChunk{Type: ChunkDone}
		close(ch)
	}()
	return ch, nil
}

func ValidateJSONSchema(schemaJSON string, data []byte) error {
	c := jsonschema.NewCompiler()

	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaJSON))
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	if err := c.AddResource("schema.json", doc); err != nil {
		return fmt.Errorf("add schema resource: %w", err)
	}

	sch, err := c.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrSchemaValidation, err)
	}

	if err := sch.Validate(v); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaValidation, err)
	}

	return nil
}
