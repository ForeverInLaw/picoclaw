package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// Config holds engine configuration.
type Config struct {
	MaxCompactIterations int    `json:"maxCompactIterations,omitempty"`
	DBPath               string `json:"dbPath"`
}

// CompleteFn is the LLM completion function type.
type CompleteFn func(ctx context.Context, prompt string, opts CompleteOptions) (string, error)

// CompleteOptions holds LLM completion parameters.
type CompleteOptions struct {
	Model       string
	MaxTokens   int
	Temperature float64
}

// AssembleInput controls context assembly.
type AssembleInput struct {
	Budget int    `json:"budget"`
	Query  string `json:"query,omitempty"`
}

// AssembleResult contains assembled context.
type AssembleResult struct {
	Messages []Message `json:"messages"`
	Summary  string    `json:"summary"` // formatted XML summaries + system prompt addition
}

const numSessionShards = 256

// Engine is the main short-term memory engine.
type Engine struct {
	store         *Store
	compaction    *CompactionEngine
	compactionMu  sync.Mutex
	assembler     *Assembler
	assemblerMu   sync.Mutex
	retrieval     *RetrievalEngine
	config        Config
	complete      CompleteFn
	sessionShards [numSessionShards]struct {
		mu sync.Mutex
	}
}

// CompactionEngine handles LLM-based summarization (defined in short_compaction.go).
type CompactionEngine struct {
	store    *Store
	config   Config
	complete CompleteFn
}

// Assembler handles budget-aware context assembly (defined in short_assembler.go).
type Assembler struct {
	store  *Store
	config Config
}

// RetrievalEngine handles search and expansion (defined in short_retrieval.go).
type RetrievalEngine struct {
	store  *Store
	config Config
}

// Store returns the underlying store for direct access.
func (r *RetrievalEngine) Store() *Store {
	return r.store
}

// NewEngine creates a new short-term memory engine.
func NewEngine(config Config, completeFn CompleteFn) (*Engine, error) {
	dir := filepath.Dir(config.DBPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	dsn := url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(config.DBPath),
		RawQuery: "_pragma=busy_timeout%2850%29&_txlock=immediate",
	}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Keep each native busy wait short: SQLite can defer context interruption
	// inside its busy handler. Source writes retry with a cancellable deadline.
	// Configure SQLite for concurrent access
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 50;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous = NORMAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set synchronous: %w", err)
	}

	if err := runSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}

	store := &Store{db: db}

	retrieval := &RetrievalEngine{store: store, config: config}

	return &Engine{
		store:      store,
		compaction: nil,
		assembler:  nil,
		retrieval:  retrieval,
		config:     config,
		complete:   completeFn,
	}, nil
}

// fnv32 computes FNV-1a 32-bit hash for session key sharding.
func fnv32(key string) uint32 {
	h := uint32(2166136261)
	for _, c := range key {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// getSessionMutex returns the sharded mutex for a session key.
func (e *Engine) getSessionMutex(sessionKey string) *sync.Mutex {
	h := fnv32(sessionKey)
	shard := h % numSessionShards
	return &e.sessionShards[shard].mu
}

// Close releases resources.
func (e *Engine) Close() error {
	if e.store != nil && e.store.db != nil {
		return e.store.db.Close()
	}
	return nil
}

// GetRetrieval returns the retrieval engine for tool implementations.
func (e *Engine) GetRetrieval() *RetrievalEngine {
	return e.retrieval
}

// Assemble builds budget-constrained context for a session.
func (e *Engine) Assemble(ctx context.Context, sessionKey string, input AssembleInput) (*AssembleResult, error) {
	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initAssemblerOnce()
	return e.assembler.Assemble(ctx, conv.ConversationID, input)
}

// Compact compresses conversation history for a session.
func (e *Engine) Compact(ctx context.Context, sessionKey string, input CompactInput) (*CompactResult, error) {
	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initCompactionOnce()
	return e.compaction.Compact(ctx, conv.ConversationID, input)
}

// CompactUntilUnder aggressively compacts until context is under budget.
// Used for emergency compaction after LLM overflow (retry reason).
func (e *Engine) CompactUntilUnder(ctx context.Context, sessionKey string, budget int) (*CompactResult, error) {
	conv, err := e.store.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	e.initCompactionOnce()
	return e.compaction.CompactUntilUnder(ctx, conv.ConversationID, budget)
}

// initCompactionOnce lazily initializes the compaction engine.
func (e *Engine) initCompactionOnce() {
	e.compactionMu.Lock()
	defer e.compactionMu.Unlock()
	if e.compaction == nil {
		e.compaction = &CompactionEngine{store: e.store, config: e.config, complete: e.complete}
	}
}

func (e *Engine) initAssemblerOnce() {
	e.assemblerMu.Lock()
	defer e.assemblerMu.Unlock()
	if e.assembler == nil {
		e.assembler = &Assembler{store: e.store, config: e.config}
	}
}

// ClearSession removes all stored data for a session (messages, summaries, context).
// If the session has no prior seahorse record, it is a no-op.
func (e *Engine) ClearSession(ctx context.Context, sessionKey string) error {
	mu := e.getSessionMutex(sessionKey)
	mu.Lock()
	defer mu.Unlock()
	conv, err := e.store.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		return err
	}
	if conv == nil {
		return nil // session never ingested, nothing to clear
	}
	return commitContext(ctx, func() error { return e.store.ClearConversation(ctx, conv.ConversationID) })
}
