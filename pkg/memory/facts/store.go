package facts

import "context"

// Store is the persistence interface for atomic facts.
//
// Implementations must be safe for concurrent use by multiple goroutines.
// Writes may be serialized internally; reads should be concurrent.
type Store interface {
	// Insert persists a new fact. f.ID is ignored; the returned ID is the
	// newly assigned primary key.
	Insert(ctx context.Context, f Fact) (int64, error)

	// Update modifies an existing fact in place. Only Value, Confidence,
	// LastSeenAt, UpdatedAt, AccessCount, Embedding, EmbeddingNorm,
	// TTLSeconds, and DeletedAt are mutable.
	Update(ctx context.Context, f Fact) error

	// SoftDelete sets deleted_at on the row.
	SoftDelete(ctx context.Context, id int64) error

	// GetByID returns a fact (including soft-deleted) by primary key.
	GetByID(ctx context.Context, id int64) (Fact, error)

	// FindByKey returns the live (deleted_at IS NULL) fact matching
	// (namespace, entity, attribute), or ErrNotFound.
	FindByKey(ctx context.Context, namespace, entity, attribute string) (Fact, error)

	// ListByNamespace returns all live facts in the given namespaces.
	// Used by the consolidator and integration tests.
	ListByNamespace(ctx context.Context, namespaces []string) ([]Fact, error)

	// KNN returns top-k facts by cosine similarity to query, restricted to
	// the given namespaces, with cosine >= minScore.
	KNN(ctx context.Context, namespaces []string, query []float32, queryNorm float64, k int, minScore float64) ([]RecallHit, error)

	// KeywordSearch is the FTS5 fallback used when embeddings are unavailable.
	KeywordSearch(ctx context.Context, namespaces []string, q string, k int) ([]RecallHit, error)

	// Close releases all resources.
	Close() error
}
