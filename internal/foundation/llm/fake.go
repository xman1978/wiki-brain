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
	sequences map[string][]FakeResponse
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

// SetResponseSequence queues distinct responses for successive calls to the
// same promptFile — needed when a test must tell a segment's initial
// extraction call apart from a later gap-fill re-extraction call, since both
// hit the same prompt file. Once the queue is drained, calls fall back to
// whatever SetResponse has configured for that file (or the "no response
// configured" error if nothing was set).
func (f *FakeClient) SetResponseSequence(promptFile string, resps []FakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sequences == nil {
		f.sequences = make(map[string][]FakeResponse)
	}
	f.sequences[promptFile] = append([]FakeResponse(nil), resps...)
}

func (f *FakeClient) nextResponse(promptFile string) (FakeResponse, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if seq, ok := f.sequences[promptFile]; ok && len(seq) > 0 {
		resp := seq[0]
		f.sequences[promptFile] = seq[1:]
		return resp, true
	}
	resp, ok := f.responses[promptFile]
	return resp, ok
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

	resp, ok := f.nextResponse(promptFile)

	if !ok {
		return "", fmt.Errorf("fake: no response configured for %q", promptFile)
	}
	return resp.Output, resp.Err
}

func (f *FakeClient) CompleteJSON(_ context.Context, promptFile string, vars map[string]string, model string) ([]byte, error) {
	f.recordCall(promptFile, model, vars)

	resp, ok := f.nextResponse(promptFile)

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

	resp, ok := f.nextResponse(promptFile)

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
