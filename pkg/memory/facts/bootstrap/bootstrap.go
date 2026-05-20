// Package bootstrap wires the facts memory subsystem together at runtime.
// It lives in its own package to avoid an import cycle between
// pkg/memory/facts and its sqlite/embed/extract/recall sub-packages.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/memory/facts/consolidate"
	"github.com/sipeed/picoclaw/pkg/memory/facts/embed"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	_ "modernc.org/sqlite"
)

// Subsystem groups the long-lived components needed to use facts memory
// at runtime. A disabled Subsystem (cfg.Enabled == false) has nil
// references everywhere and Close is a no-op.
type Subsystem struct {
	Store        facts.Store
	Recaller     *recall.Recaller
	Worker       *extract.Worker
	AsyncWorker  *extract.AsyncWorker
	Consolidator *consolidate.Consolidator
	Logger       *extract.Logger
	BotUsername  string

	closeFn func() error
}

// Close releases the SQLite handle (and anything else added later).
func (s *Subsystem) Close() error {
	if s == nil || s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}

// Bootstrap instantiates the facts subsystem from config. Returns a no-op
// Subsystem when cfg.Enabled is false. The caller is responsible for
// calling subsystem.AsyncWorker.Start(ctx) and StartDecayTicker.
func Bootstrap(ctx context.Context, cfg facts.Config, prov embed.EmbeddingProvider, llm extract.ExtractionLLM, botUsername string) (*Subsystem, error) {
	if !cfg.Enabled {
		return &Subsystem{}, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("facts: open sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("facts: pragmas: %w", err)
	}
	if err := factsqlite.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("facts: migrate: %w", err)
	}

	store := factsqlite.NewStore(db)

	var embedder *embed.Embedder
	if prov != nil {
		embedder = embed.NewEmbedder(prov, cfg.EmbeddingModel, cfg.EmbeddingDim)
	} else {
		embedder = embed.NewEmbedder(embed.NullProvider{}, cfg.EmbeddingModel, cfg.EmbeddingDim)
	}

	recaller := recall.NewRecaller(store, embedder, cfg.TopK, cfg.RecallMinScore)
	logger := &extract.Logger{DB: db}
	worker := extract.NewWorker(store, embedder, llm, logger, "", "tg:bot:"+botUsername)
	async := extract.NewAsyncWorker(worker, 64)
	cons := consolidate.NewConsolidator(store, consolidate.Config{
		DecayWindow:   cfg.DecayWindow(),
		MinConfidence: cfg.MinConfidence,
	})

	return &Subsystem{
		Store:        store,
		Recaller:     recaller,
		Worker:       worker,
		AsyncWorker:  async,
		Consolidator: cons,
		Logger:       logger,
		BotUsername:  botUsername,
		closeFn:      store.Close,
	}, nil
}

// StartDecayTicker runs Consolidator.Run on the provided interval. Stops
// when ctx is cancelled. Pass interval <= 0 to disable.
func (s *Subsystem) StartDecayTicker(ctx context.Context, interval time.Duration, namespaces []string) {
	if s == nil || s.Consolidator == nil || interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = s.Consolidator.Run(ctx, namespaces)
			}
		}
	}()
}
