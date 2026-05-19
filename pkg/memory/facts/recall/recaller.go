package recall

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Embedder is the minimal Embedder contract the recaller depends on.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, float64, error)
}

// Recaller is the synchronous recall entry point used by channel handlers.
type Recaller struct {
	store    facts.Store
	embed    Embedder
	topK     int
	minScore float64
}

func NewRecaller(s facts.Store, e Embedder, topK int, minScore float64) *Recaller {
	return &Recaller{store: s, embed: e, topK: topK, minScore: minScore}
}

// Recall returns up to topK facts whose cosine to the embedded input is
// >= minScore, scoped by the given namespaces. Falls back to keyword search
// if embedding fails.
func (r *Recaller) Recall(ctx context.Context, namespaces []string, input string) ([]facts.RecallHit, error) {
	vec, norm, err := r.embed.Embed(ctx, input)
	if err != nil {
		return r.store.KeywordSearch(ctx, namespaces, input, r.topK)
	}
	return r.store.KNN(ctx, namespaces, vec, norm, r.topK, r.minScore)
}
