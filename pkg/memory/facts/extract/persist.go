package extract

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Tunables.
const (
	dedupeCosineThreshold = 0.9
	confidenceBumpStep    = 0.05
)

func (w *Worker) persist(ctx context.Context, ef ExtractedFact, j Job) error {
	canonical := strings.ToLower(strings.TrimSpace(ef.Entity + " " + ef.Attribute + " " + ef.Value))
	vec, norm, err := w.embed.Embed(ctx, canonical)
	if err != nil {
		return err
	}

	existing, err := w.store.FindByKey(ctx, w.namespace, ef.Entity, ef.Attribute)
	now := time.Now().UTC()

	switch {
	case errors.Is(err, facts.ErrNotFound):
		_, err := w.store.Insert(ctx, facts.Fact{
			Namespace:     w.namespace,
			Entity:        ef.Entity,
			Attribute:     ef.Attribute,
			Value:         ef.Value,
			Confidence:    clamp01(ef.Confidence),
			SourceMsgRef:  j.SessionKey,
			CreatedAt:     now,
			UpdatedAt:     now,
			LastSeenAt:    now,
			Embedding:     vec,
			EmbeddingNorm: norm,
		})
		return err

	case err != nil:
		return err

	default:
		// Existing live fact. Compute cosine of the new embedding vs the
		// stored one. If similar enough, treat as a duplicate (bump
		// confidence); otherwise treat as a contradiction.
		sim := 0.0
		if len(existing.Embedding) == len(vec) && existing.EmbeddingNorm > 0 {
			sim = facts.Cosine(vec, norm, existing.Embedding, existing.EmbeddingNorm)
		}
		if sim >= dedupeCosineThreshold {
			existing.Confidence = clamp01(existing.Confidence + confidenceBumpStep)
			existing.LastSeenAt = now
			existing.UpdatedAt = now
			existing.AccessCount++
			return w.store.Update(ctx, existing)
		}
		// Contradiction — soft-delete the old, insert the new.
		if err := w.store.SoftDelete(ctx, existing.ID); err != nil {
			return err
		}
		_, err := w.store.Insert(ctx, facts.Fact{
			Namespace:     w.namespace,
			Entity:        ef.Entity,
			Attribute:     ef.Attribute,
			Value:         ef.Value,
			Confidence:    clamp01(ef.Confidence),
			SourceMsgRef:  j.SessionKey,
			CreatedAt:     now,
			UpdatedAt:     now,
			LastSeenAt:    now,
			Embedding:     vec,
			EmbeddingNorm: norm,
		})
		return err
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
