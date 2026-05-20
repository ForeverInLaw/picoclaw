package extract

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	_ "modernc.org/sqlite"
)

type fakeLLM struct {
	resp string
	err  error
}

func (f *fakeLLM) ExtractFacts(ctx context.Context, sys, user string) (string, error) {
	return f.resp, f.err
}

type stubEmbed struct{}

func (s *stubEmbed) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return []float32{1, 0, 0}, 1.0, nil
}

func newWorkerWithLLM(t *testing.T, llm *fakeLLM) (*Worker, *Logger, *factsqlite.Store) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	t.Cleanup(func() { db.Close() })
	if err := factsqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := factsqlite.NewStore(db)
	logger := &Logger{DB: db}
	w := NewWorker(store, &stubEmbed{}, llm, logger, "tg:user:1", "tg:bot:test")
	return w, logger, store
}

func TestWorker_HappyPath(t *testing.T) {
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.9}]}`}
	w, logger, store := newWorkerWithLLM(t, llm)

	w.Run(context.Background(), Job{
		SessionKey: "tg:user:1",
		Window: []protocoltypes.Message{
			{Role: "user", Content: "Андрей сказал что любит грейпфрут"},
		},
		StartIdx: 0, EndIdx: 1,
	})

	got, err := store.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if err != nil {
		t.Fatalf("FindByKey: %v", err)
	}
	if got.Value != "грейпфрут" {
		t.Fatalf("value: %q", got.Value)
	}
	if got.Confidence < 0.89 {
		t.Fatalf("confidence: %v", got.Confidence)
	}
	idx, _ := logger.LastProcessed(context.Background(), "tg:user:1")
	if idx != 1 {
		t.Fatalf("LastProcessed: %d", idx)
	}
}

func TestWorker_DedupeRaisesConfidence(t *testing.T) {
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.7}]}`}
	w, logger, store := newWorkerWithLLM(t, llm)

	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 1})
	_ = logger.Mark(context.Background(), "tg:user:1", 0)
	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 2})

	f, _ := store.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if f.Confidence <= 0.7 {
		t.Fatalf("confidence should have been bumped, got %v", f.Confidence)
	}
}

func TestWorker_Contradiction(t *testing.T) {
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.7}]}`}
	w, logger, store := newWorkerWithLLM(t, llm)

	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 1})
	_ = logger.Mark(context.Background(), "tg:user:1", 0)

	// Different stored embedding so the cosine threshold is missed and we
	// take the contradiction branch.
	llm.resp = `{"facts":[{"entity":"Андрей","attribute":"likes","value":"арбуз","confidence":0.9}]}`
	// Switch stub to return an orthogonal embedding so cosine vs stored = 0.
	w.embed = orthoEmbed{}
	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 2})

	f, _ := store.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if f.Value != "арбуз" {
		t.Fatalf("contradiction not resolved, got %q", f.Value)
	}
}

// orthoEmbed yields a vector orthogonal to {1,0,0} so cosine = 0.
type orthoEmbed struct{}

func (orthoEmbed) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return []float32{0, 1, 0}, 1.0, nil
}
