// Package embed wraps PicoClaw's existing provider routing with the minimal
// embedding contract used by pkg/memory/facts.
package embed

import (
	"context"
	"fmt"
	"math"
)

// EmbeddingProvider is the minimal interface for an external embedding source.
// PicoClaw's provider routing implements this for OpenAI-compatible
// /embeddings endpoints.
type EmbeddingProvider interface {
	Embed(ctx context.Context, model, text string) ([]float32, error)
}

// Embedder wraps a provider with a model slug + dimension contract.
type Embedder struct {
	provider  EmbeddingProvider
	modelSlug string
	dim       int
}

// NewEmbedder constructs an Embedder pinned to a specific model slug and
// expected vector dimension.
func NewEmbedder(p EmbeddingProvider, modelSlug string, dim int) *Embedder {
	return &Embedder{provider: p, modelSlug: modelSlug, dim: dim}
}

// Embed returns the vector and its L2 norm. Returns an error if the
// provider's response length does not match the configured dimension.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	v, err := e.provider.Embed(ctx, e.modelSlug, text)
	if err != nil {
		return nil, 0, err
	}
	if len(v) != e.dim {
		return nil, 0, fmt.Errorf("embed: dim mismatch: want %d got %d", e.dim, len(v))
	}
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return v, math.Sqrt(s), nil
}
