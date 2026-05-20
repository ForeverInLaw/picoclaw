package recall

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
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
// if embedding fails. Bumps access_count and last_seen_at on every hit so
// downstream consolidation knows the fact stays useful.
func (r *Recaller) Recall(ctx context.Context, namespaces []string, input string) ([]facts.RecallHit, error) {
	hits, err := r.lookup(ctx, namespaces, input)
	if err != nil {
		return nil, err
	}
	r.bumpAccess(ctx, hits)
	return hits, nil
}

func (r *Recaller) lookup(ctx context.Context, namespaces []string, input string) ([]facts.RecallHit, error) {
	vec, norm, err := r.embed.Embed(ctx, input)
	if err != nil {
		logger.WarnCF("memory.facts", "recall: embedding failed, falling back to keyword search", map[string]any{
			"namespaces": namespaces, "error": err.Error(),
		})
		return r.store.KeywordSearch(ctx, namespaces, input, r.topK)
	}
	return r.store.KNN(ctx, namespaces, vec, norm, r.topK, r.minScore)
}

func (r *Recaller) bumpAccess(ctx context.Context, hits []facts.RecallHit) {
	now := time.Now().UTC()
	for _, h := range hits {
		f := h.Fact
		f.AccessCount++
		f.LastSeenAt = now
		f.UpdatedAt = now
		// Best-effort: a failed bump must not poison the user-facing turn.
		_ = r.store.Update(ctx, f)
	}
}
