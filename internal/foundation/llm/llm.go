package llm

import (
	"context"
	"errors"
)

var (
	ErrTimeout          = errors.New("llm: timeout")
	ErrSchemaValidation = errors.New("llm: schema validation failed")
	ErrModelError       = errors.New("llm: model error")
	ErrNotConfigured    = errors.New("llm: not configured")
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
	// CompleteImage is Complete's multimodal counterpart: the prompt file's
	// User section is sent alongside images (in the order given) as a single
	// multipart chat message, for OCR/vision use cases (see doc_convert
	// purpose, docs/impl/v1/local-file-convert.md §10).
	CompleteImage(ctx context.Context, promptFile string, vars map[string]string, images []ImageInput, purpose string) (string, error)
}

// ImageInput is one image attached to a CompleteImage call.
type ImageInput struct {
	Data     []byte
	MimeType string // e.g. "image/png", "image/jpeg"
}
