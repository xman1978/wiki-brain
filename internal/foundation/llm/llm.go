package llm

import (
	"context"
	"errors"
)

var (
	ErrTimeout          = errors.New("llm: timeout")
	ErrSchemaValidation = errors.New("llm: schema validation failed")
	ErrModelError       = errors.New("llm: model error")
)

type StreamChunkType int

const (
	ChunkThinking StreamChunkType = iota
	ChunkContent
	ChunkDone
	ChunkError
	ChunkPhase
)

type StreamChunk struct {
	Type    StreamChunkType
	Content string
	Err     error
}

type LLMClient interface {
	Complete(ctx context.Context, promptFile string, vars map[string]string, model string) (string, error)
	CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error)
	CompleteStream(ctx context.Context, promptFile string, vars map[string]string, model string) (<-chan StreamChunk, error)
}
