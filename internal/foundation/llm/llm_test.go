package llm

import (
	"context"
	"testing"
)

func TestFakeClientComplete(t *testing.T) {
	client := NewFakeClient()
	client.SetResponse("test.md", FakeResponse{Output: "hello world"})

	result, err := client.Complete(context.Background(), "test.md", map[string]string{"key": "val"}, "default")
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if result != "hello world" {
		t.Errorf("got %q, want %q", result, "hello world")
	}

	calls := client.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].PromptFile != "test.md" {
		t.Errorf("promptFile = %q, want test.md", calls[0].PromptFile)
	}
}

func TestFakeClientCompleteError(t *testing.T) {
	client := NewFakeClient()
	client.SetResponse("test.md", FakeResponse{Err: ErrTimeout})

	_, err := client.Complete(context.Background(), "test.md", nil, "default")
	if err != ErrTimeout {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestFakeClientCompleteJSON(t *testing.T) {
	client := NewFakeClient()
	client.SetResponse("test.md", FakeResponse{Output: `{"units":[],"points":[]}`})

	data, err := client.CompleteJSON(context.Background(), "test.md", nil, "default")
	if err != nil {
		t.Fatalf("CompleteJSON failed: %v", err)
	}
	if string(data) != `{"units":[],"points":[]}` {
		t.Errorf("unexpected output: %s", data)
	}
}

func TestFakeClientCompleteJSONInvalid(t *testing.T) {
	client := NewFakeClient()
	client.SetResponse("test.md", FakeResponse{Output: "not json"})

	_, err := client.CompleteJSON(context.Background(), "test.md", nil, "default")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFakeClientNoResponse(t *testing.T) {
	client := NewFakeClient()

	_, err := client.Complete(context.Background(), "missing.md", nil, "default")
	if err == nil {
		t.Fatal("expected error for unconfigured prompt")
	}
}

func TestParsePrompt(t *testing.T) {
	content := `---
version: v1
---

## System

You are a helpful assistant.

## User

Content: {{content}}
Title: {{title}}

## Schema

` + "```json\n" + `{
  "type": "object",
  "required": ["result"],
  "properties": {
    "result": {"type": "string"}
  }
}
` + "```"

	p, err := ParsePrompt(content, map[string]string{
		"content": "hello",
		"title":   "test",
	})
	if err != nil {
		t.Fatalf("ParsePrompt failed: %v", err)
	}

	if p.Version != "v1" {
		t.Errorf("version = %q, want v1", p.Version)
	}
	if p.System != "You are a helpful assistant." {
		t.Errorf("system = %q", p.System)
	}
	if p.User != "Content: hello\nTitle: test" {
		t.Errorf("user = %q", p.User)
	}
	if p.Schema == "" {
		t.Error("schema is empty")
	}
}

func TestValidateJSONSchema(t *testing.T) {
	schema := `{
		"type": "object",
		"required": ["name"],
		"properties": {
			"name": {"type": "string"}
		}
	}`

	if err := ValidateJSONSchema(schema, []byte(`{"name":"test"}`)); err != nil {
		t.Errorf("valid data failed: %v", err)
	}

	if err := ValidateJSONSchema(schema, []byte(`{"age":10}`)); err == nil {
		t.Error("expected validation error for missing required field")
	}

	if err := ValidateJSONSchema(schema, []byte(`not json`)); err == nil {
		t.Error("expected error for invalid JSON")
	}
}
