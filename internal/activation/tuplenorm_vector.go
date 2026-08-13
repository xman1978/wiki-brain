package activation

import (
	"fmt"
	"log/slog"
	"math"

	"github.com/MichaelAyles/goformer"
)

// VectorEmbedder is TupleNormalizer's Tier 2.5 dependency: turns a query or
// candidate tuple string into a fixed-length embedding vector. Kept as an
// interface so the LLM-free vector tier degrades cleanly (nil embedder ⇒
// Tier 2.5 no-ops, falls through straight to Tier 3) whenever no backing
// implementation is wired — see NewGoformerEmbedder below and
// docs/impl/v1/retrieval.md 步骤 2.
type VectorEmbedder interface {
	Embed(text string) ([]float32, error)
}

// goformerEmbedder wraps github.com/MichaelAyles/goformer's pure-Go
// BERT-family embedding model. Loaded once at startup from cfg.VectorModelDir
// (a HuggingFace safetensors model directory); Embed is safe for concurrent
// use (goformer.Model itself documents this).
type goformerEmbedder struct {
	model *goformer.Model
}

// NewGoformerEmbedder loads the embedding model from modelDir. Returns
// (nil, err) on failure — callers must not panic on error, just log a
// warning and leave vector matching disabled (Tier 2.5 degrades to a no-op
// when the embedder is nil), per the asymmetric-safety design: vector match
// is an optimization, never a correctness requirement.
func NewGoformerEmbedder(modelDir string) (VectorEmbedder, error) {
	if modelDir == "" {
		return nil, fmt.Errorf("activation: vector_model_dir is empty")
	}
	model, err := goformer.Load(modelDir)
	if err != nil {
		return nil, fmt.Errorf("activation: goformer.Load(%q): %w", modelDir, err)
	}
	slog.Info("activation: goformer embedder loaded", "model_dir", modelDir, "dims", model.Dims())
	return &goformerEmbedder{model: model}, nil
}

func (e *goformerEmbedder) Embed(text string) ([]float32, error) {
	return e.model.Embed(text)
}

// cosineSimilarity computes the cosine similarity of two equal-length
// vectors. Returns 0 for empty/mismatched-length inputs rather than erroring
// — callers treat that as "no signal", which is the safe default for a
// reject-only tier.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
