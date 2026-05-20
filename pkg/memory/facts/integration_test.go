package facts_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory/facts/embed"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	_ "modernc.org/sqlite"
)

type scriptedLLM struct{ resp string }

func (s *scriptedLLM) ExtractFacts(ctx context.Context, sys, user string) (string, error) {
	return s.resp, nil
}

type scriptedEmbed struct{}

// Embed hashes the input text into a deterministic 8-dim vector that
// changes direction with the content (not just magnitude). Used so the
// kNN path exercises real cosine arithmetic without an external service.
func (scriptedEmbed) Embed(ctx context.Context, model, text string) ([]float32, error) {
	v := make([]float32, 8)
	v[0] = float32(len(text))
	for i, r := range text {
		v[1+(int(r)+i)%7] += float32(int(r))
	}
	return v, nil
}

func TestE2E_ExtractThenRecall(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	if err := factsqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := factsqlite.NewStore(db)
	embedder := embed.NewEmbedder(scriptedEmbed{}, "stub", 8)
	llm := &scriptedLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрутовый сок","confidence":0.9}]}`}
	logger := &extract.Logger{DB: db}
	worker := extract.NewWorker(store, embedder, llm, logger, "tg:chat:-100", "tg:bot:c0md_bot")

	// Round 1: extract from a scripted conversation.
	worker.Run(context.Background(), extract.Job{
		SessionKey: "tg:chat:-100",
		Window: []protocoltypes.Message{
			{Role: "user", Content: "Андрей сказал что любит грейпфрутовый сок"},
		},
		StartIdx: 0, EndIdx: 1,
	})

	// Round 2: recall in a "later" turn.
	r := recall.NewRecaller(store, &embedderAdapter{e: embedder}, 5, 0.0)
	hits, err := r.Recall(context.Background(), []string{"tg:chat:-100"}, "что любит Андрей?")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if hits[0].Fact.Value != "грейпфрутовый сок" {
		t.Fatalf("wrong fact: %q", hits[0].Fact.Value)
	}
}

func TestE2E_ContradictionAcrossRounds(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)
	embedder := embed.NewEmbedder(scriptedEmbed{}, "stub", 8)
	logger := &extract.Logger{DB: db}

	llm := &scriptedLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.7}]}`}
	worker := extract.NewWorker(store, embedder, llm, logger, "tg:chat:-100", "tg:bot:c0md_bot")
	worker.Run(context.Background(), extract.Job{
		SessionKey: "tg:chat:-100",
		Window:     []protocoltypes.Message{{Role: "user", Content: "грейпфрут"}},
		EndIdx:     1,
	})
	_ = logger.Mark(context.Background(), "tg:chat:-100", 0)

	// Round 2: contradictory value with different text → different embedding.
	llm.resp = `{"facts":[{"entity":"Андрей","attribute":"likes","value":"арбуз","confidence":0.9}]}`
	worker.Run(context.Background(), extract.Job{
		SessionKey: "tg:chat:-100",
		Window:     []protocoltypes.Message{{Role: "user", Content: "арбуз"}},
		EndIdx:     2,
	})

	f, err := store.FindByKey(context.Background(), "tg:chat:-100", "Андрей", "likes")
	if err != nil {
		t.Fatal(err)
	}
	if f.Value != "арбуз" {
		t.Fatalf("contradiction not resolved, got %q", f.Value)
	}
}

// embedderAdapter exposes embed.Embedder under the Recaller's Embedder
// interface (different package shape).
type embedderAdapter struct{ e *embed.Embedder }

func (a *embedderAdapter) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return a.e.Embed(ctx, text)
}
