package sqlite

import (
	"context"
	"sort"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

func (s *Store) KNN(ctx context.Context, namespaces []string, query []float32, queryNorm float64, k int, minScore float64) ([]facts.RecallHit, error) {
	if len(query) == 0 || queryNorm == 0 || k <= 0 {
		return nil, nil
	}
	rows, err := s.ListByNamespace(ctx, namespaces)
	if err != nil {
		return nil, err
	}
	hits := make([]facts.RecallHit, 0, len(rows))
	for _, f := range rows {
		if len(f.Embedding) != len(query) || f.EmbeddingNorm == 0 {
			continue
		}
		score := facts.Cosine(query, queryNorm, f.Embedding, f.EmbeddingNorm)
		if score < minScore {
			continue
		}
		hits = append(hits, facts.RecallHit{Fact: f, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}
