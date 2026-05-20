package embed

import (
	"context"
	"errors"
)

// ErrNoEmbeddings is returned by NullProvider so callers fall back to
// keyword recall and string-equality dedup.
var ErrNoEmbeddings = errors.New("embed: embeddings disabled")

// NullProvider always returns ErrNoEmbeddings. Use it as a placeholder
// when no real embedding backend is wired up — recall and persistence
// degrade gracefully without it.
type NullProvider struct{}

func (NullProvider) Embed(ctx context.Context, model, text string) ([]float32, error) {
	return nil, ErrNoEmbeddings
}
