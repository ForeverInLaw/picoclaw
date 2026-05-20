package recall

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	_ "modernc.org/sqlite"
)

type stubEmbed struct {
	v []float32
	n float64
}

func (s *stubEmbed) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return s.v, s.n, nil
}

func TestRecaller_PrefersHigherCosine(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)

	now := time.Now().UTC()
	_, _ = store.Insert(context.Background(), facts.Fact{
		Namespace: "tg:user:1", Entity: "A", Attribute: "k", Value: "match",
		Embedding: []float32{1, 0, 0}, EmbeddingNorm: 1.0,
		CreatedAt: now, UpdatedAt: now, LastSeenAt: now,
		SourceMsgRef: "m",
	})
	_, _ = store.Insert(context.Background(), facts.Fact{
		Namespace: "tg:user:1", Entity: "B", Attribute: "k", Value: "other",
		Embedding: []float32{0, 1, 0}, EmbeddingNorm: 1.0,
		CreatedAt: now, UpdatedAt: now, LastSeenAt: now,
		SourceMsgRef: "m",
	})

	r := NewRecaller(store, &stubEmbed{v: []float32{1, 0, 0}, n: 1.0}, 5, 0.5)
	hits, err := r.Recall(context.Background(), []string{"tg:user:1"}, "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Fact.Value != "match" {
		t.Fatalf("bad hits: %+v", hits)
	}
}
