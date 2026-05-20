package consolidate

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

func newStore(t *testing.T) *factsqlite.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	t.Cleanup(func() { db.Close() })
	if err := factsqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return factsqlite.NewStore(db)
}

func TestConsolidate_DecaysOldFacts(t *testing.T) {
	store := newStore(t)

	old := time.Now().Add(-60 * 24 * time.Hour).UTC()
	id, _ := store.Insert(context.Background(), facts.Fact{
		Namespace: "tg:user:1", Entity: "X", Attribute: "Y", Value: "Z",
		Confidence: 0.4, SourceMsgRef: "m",
		CreatedAt: old, UpdatedAt: old, LastSeenAt: old,
	})

	c := NewConsolidator(store, Config{
		DecayWindow:   30 * 24 * time.Hour,
		MinConfidence: 0.15,
	})
	if err := c.Run(context.Background(), []string{"tg:user:1"}); err != nil {
		t.Fatal(err)
	}
	f, _ := store.GetByID(context.Background(), id)
	if f.Confidence > 0.21 {
		t.Fatalf("confidence not halved: %v", f.Confidence)
	}
}

func TestConsolidate_SoftDeletesBelowMin(t *testing.T) {
	store := newStore(t)
	old := time.Now().Add(-60 * 24 * time.Hour).UTC()
	id, _ := store.Insert(context.Background(), facts.Fact{
		Namespace: "tg:user:1", Entity: "X", Attribute: "Y", Value: "Z",
		Confidence: 0.2, SourceMsgRef: "m",
		CreatedAt: old, UpdatedAt: old, LastSeenAt: old,
	})
	c := NewConsolidator(store, Config{
		DecayWindow:   30 * 24 * time.Hour,
		MinConfidence: 0.15,
	})
	_ = c.Run(context.Background(), []string{"tg:user:1"})
	f, _ := store.GetByID(context.Background(), id)
	if f.DeletedAt.IsZero() {
		t.Fatal("expected soft-delete")
	}
}
