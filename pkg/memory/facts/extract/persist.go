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
	// Skip obvious placeholder rows the LLM sometimes emits when it mirrors
	// the prompt's example schema verbatim.
	if isPlaceholder(ef.Entity) || isPlaceholder(ef.Attribute) || isPlaceholder(ef.Value) {
		return nil
	}
	namespace := j.Namespace
	if namespace == "" {
		namespace = w.namespace
	}
	if namespace == "" {
		// Without a namespace we cannot scope the fact; drop it.
		return nil
	}
	canonical := strings.ToLower(strings.TrimSpace(ef.Entity + " " + ef.Attribute + " " + ef.Value))
	// Embedding is best-effort. When the embedder is unavailable we still
	// persist the fact and fall back to string-equality for dedup.
	vec, norm, embErr := w.embed.Embed(ctx, canonical)
	if embErr != nil {
		vec = nil
		norm = 0
	}

	existing, err := w.store.FindByKey(ctx, namespace, ef.Entity, ef.Attribute)
	now := time.Now().UTC()

	switch {
	case errors.Is(err, facts.ErrNotFound):
		_, err := w.store.Insert(ctx, facts.Fact{
			Namespace:     namespace,
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
		// stored one when both have embeddings; otherwise fall back to
		// case-insensitive value equality.
		var isDup bool
		if len(existing.Embedding) > 0 && len(vec) > 0 &&
			len(existing.Embedding) == len(vec) && existing.EmbeddingNorm > 0 {
			sim := facts.Cosine(vec, norm, existing.Embedding, existing.EmbeddingNorm)
			isDup = sim >= dedupeCosineThreshold
		} else {
			isDup = strings.EqualFold(strings.TrimSpace(existing.Value), strings.TrimSpace(ef.Value))
		}
		if isDup {
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
			Namespace:     namespace,
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

// isPlaceholder reports whether the value is one of the known placeholder
// strings that LLMs occasionally mirror from the prompt schema instead of
// returning real data.
func isPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	// Common placeholder shapes: "...", "…", "string", "<entity>".
	switch t {
	case "...", "…":
		return true
	case "string", "name", "value", "attribute", "entity":
		return true
	}
	if strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
		return true
	}
	return false
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
