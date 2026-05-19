// Package consolidate runs periodic maintenance over the facts store:
// confidence decay and soft-deletion of stale, low-confidence facts.
package consolidate

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

type Config struct {
	DecayWindow   time.Duration
	MinConfidence float64
}

type Consolidator struct {
	store facts.Store
	cfg   Config
}

func NewConsolidator(s facts.Store, c Config) *Consolidator {
	return &Consolidator{store: s, cfg: c}
}

// Run walks live facts in the given namespaces. Facts older than DecayWindow
// have their confidence halved; if they drop below MinConfidence they are
// soft-deleted.
func (c *Consolidator) Run(ctx context.Context, namespaces []string) error {
	all, err := c.store.ListByNamespace(ctx, namespaces)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-c.cfg.DecayWindow)
	now := time.Now().UTC()
	for _, f := range all {
		if !f.LastSeenAt.Before(cutoff) {
			continue
		}
		f.Confidence = f.Confidence * 0.5
		f.UpdatedAt = now
		if f.Confidence < c.cfg.MinConfidence {
			if err := c.store.SoftDelete(ctx, f.ID); err != nil {
				return err
			}
			continue
		}
		if err := c.store.Update(ctx, f); err != nil {
			return err
		}
	}
	return nil
}
