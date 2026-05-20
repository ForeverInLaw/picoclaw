# Telegram Facts Memory — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add mem0-style atomic-facts memory to PicoClaw's Telegram channel with synchronous semantic recall and async extraction.

**Architecture:** New `pkg/memory/facts` subsystem, independent of the existing `pkg/memory` conversation store. Pure-Go SQLite (`modernc.org/sqlite`) with BLOB embeddings and namespace-scoped brute-force kNN. Async LLM extraction worker, daily decay cron, gated by `agents.memory.facts.enabled` config flag.

**Tech Stack:** Go 1.25, `modernc.org/sqlite`, existing PicoClaw provider routing for `/embeddings`, existing `pkg/cron` for scheduled decay, build tags `goolm,stdjson`, `CGO_ENABLED=0`.

**Spec:** `docs/design/2026-05-20-telegram-memory-design.md`

**Branch:** `feat/telegram-memory` (off `merge/origin-main-into-dev` @ `7ecb62d`)

---

## Conventions used throughout this plan

- Commits use Conventional Commits. **No Claude attribution / Co-Authored-By trailers.**
- Each task ends with a commit. Tests pass before the commit step.
- Test files live alongside the code they test (`foo.go` → `foo_test.go`).
- All packages export through `pkg/memory/facts`. Sub-packages are internal-by-convention even if not in an `internal/` dir.
- Build verification: `go build -tags "goolm,stdjson" ./...` must pass after each task.
- Unit-test command: `go test ./pkg/memory/facts/... -count=1`.

---

## Task 1: Add modernc.org/sqlite dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `pkg/memory/facts/doc.go`

- [ ] **Step 1: Add dependency**

Run:
```
go get modernc.org/sqlite@latest
```

Expected: `go.mod` gains `modernc.org/sqlite vX.Y.Z`, `go.sum` updates.

- [ ] **Step 2: Create package doc as anchor**

Create `pkg/memory/facts/doc.go`:
```go
// Package facts provides mem0-style atomic-fact memory for PicoClaw agents.
//
// The package is independent of the conversation history store in
// pkg/memory. Facts are atomic (entity, attribute, value) tuples scoped
// by namespace, with embedding-backed semantic recall and async LLM
// extraction.
//
// See docs/design/2026-05-20-telegram-memory-design.md for the design spec.
package facts
```

- [ ] **Step 3: Verify build still passes**

Run:
```
go build -tags "goolm,stdjson" ./...
```

Expected: success.

- [ ] **Step 4: Commit**

```
git add go.mod go.sum pkg/memory/facts/doc.go
git commit -m "feat(memory/facts): add facts package and modernc.org/sqlite dep"
```

---

## Task 2: FactStore interface and core types

**Files:**
- Create: `pkg/memory/facts/types.go`
- Create: `pkg/memory/facts/store.go`
- Create: `pkg/memory/facts/errors.go`

- [ ] **Step 1: Define types**

Create `pkg/memory/facts/types.go`:
```go
package facts

import "time"

// Fact is an atomic memory tuple.
type Fact struct {
	ID            int64
	Namespace     string
	Entity        string
	Attribute     string
	Value         string
	Confidence    float64
	SourceMsgRef  string
	SourceActor   string    // empty when bot-inferred
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSeenAt    time.Time
	AccessCount   int64
	TTLSeconds    int64     // 0 = use config default
	DeletedAt     time.Time // zero when not deleted
	Embedding     []float32 // nil if not yet embedded
	EmbeddingNorm float64
}

// Namespace formatting helpers.
const (
	NSPrefixTGChat = "tg:chat:"
	NSPrefixTGUser = "tg:user:"
	NSPrefixTGBot  = "tg:bot:"
)

// RecallHit is a kNN search result.
type RecallHit struct {
	Fact  Fact
	Score float64
}
```

- [ ] **Step 2: Define sentinel errors**

Create `pkg/memory/facts/errors.go`:
```go
package facts

import "errors"

var (
	ErrNotFound        = errors.New("facts: not found")
	ErrMissingEmbedding = errors.New("facts: embedding not set")
	ErrInvalidConfig    = errors.New("facts: invalid config")
)
```

- [ ] **Step 3: Define the FactStore interface**

Create `pkg/memory/facts/store.go`:
```go
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
```

- [ ] **Step 4: Build check**

Run:
```
go build -tags "goolm,stdjson" ./...
```

Expected: success.

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/
git commit -m "feat(memory/facts): define Store interface and core types"
```

---

## Task 3: SQLite migrations infrastructure

**Files:**
- Create: `pkg/memory/facts/sqlite/migrate.go`
- Create: `pkg/memory/facts/sqlite/migrations/001_init.sql`
- Create: `pkg/memory/facts/sqlite/migrate_test.go`

- [ ] **Step 1: Write the failing migration test**

Create `pkg/memory/facts/sqlite/migrate_test.go`:
```go
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrate_FreshDB(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='facts'`).Scan(&name)
	if err != nil {
		t.Fatalf("facts table missing: %v", err)
	}
	if name != "facts" {
		t.Fatalf("got %q want facts", name)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("Migrate iter %d: %v", i, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1 -run TestMigrate
```

Expected: FAIL (package does not compile — `Migrate` not defined).

- [ ] **Step 3: Implement Migrate**

Create `pkg/memory/facts/sqlite/migrate.go`:
```go
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate brings the database schema up to the latest version. Idempotent.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embed: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var version int
		_, err := fmt.Sscanf(name, "%03d_", &version)
		if err != nil {
			return fmt.Errorf("parse version from %q: %w", name, err)
		}

		var applied int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version WHERE version = ?`, version).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check version %d: %w", version, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Create migration 001 (initial schema)**

Create `pkg/memory/facts/sqlite/migrations/001_init.sql`:
```sql
CREATE TABLE facts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace       TEXT    NOT NULL,
  entity          TEXT    NOT NULL,
  attribute       TEXT    NOT NULL,
  value           TEXT    NOT NULL,
  confidence      REAL    NOT NULL DEFAULT 0.7,
  source_msg_ref  TEXT    NOT NULL,
  source_actor    TEXT,
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL,
  last_seen_at    TIMESTAMP NOT NULL,
  access_count    INTEGER NOT NULL DEFAULT 0,
  ttl_seconds     INTEGER,
  deleted_at      TIMESTAMP,
  embedding       BLOB,
  embedding_norm  REAL
);

CREATE INDEX idx_facts_ns_entity_attr
  ON facts(namespace, entity, attribute)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_facts_ns_decay
  ON facts(namespace, last_seen_at)
  WHERE deleted_at IS NULL;

CREATE TABLE extraction_log (
  session_key            TEXT PRIMARY KEY,
  last_processed_msg_idx INTEGER NOT NULL,
  last_run_at            TIMESTAMP NOT NULL
);

CREATE TABLE failed_extractions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_key TEXT NOT NULL,
  msg_range   TEXT NOT NULL,
  error       TEXT NOT NULL,
  failed_at   TIMESTAMP NOT NULL
);
```

- [ ] **Step 5: Run tests**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1 -run TestMigrate
```

Expected: PASS, two tests green.

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/sqlite/
git commit -m "feat(memory/facts): sqlite migrations infra + initial schema"
```

---

## Task 4: Migration 002 — FTS5 virtual table

**Files:**
- Create: `pkg/memory/facts/sqlite/migrations/002_fts.sql`
- Modify: `pkg/memory/facts/sqlite/migrate_test.go` (extend test)

- [ ] **Step 1: Extend the migration test**

Append to `pkg/memory/facts/sqlite/migrate_test.go`:
```go
func TestMigrate_FTSTable(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='facts_fts'`)
	var name string
	if err := row.Scan(&name); err != nil {
		t.Fatalf("facts_fts missing: %v", err)
	}
}
```

- [ ] **Step 2: Run — verify it fails**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1 -run TestMigrate_FTSTable
```

Expected: FAIL (no facts_fts table yet).

- [ ] **Step 3: Add migration 002**

Create `pkg/memory/facts/sqlite/migrations/002_fts.sql`:
```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(
  entity, attribute, value,
  content='facts', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER facts_fts_ai AFTER INSERT ON facts BEGIN
  INSERT INTO facts_fts(rowid, entity, attribute, value)
  VALUES (new.id, new.entity, new.attribute, new.value);
END;

CREATE TRIGGER facts_fts_ad AFTER DELETE ON facts BEGIN
  INSERT INTO facts_fts(facts_fts, rowid, entity, attribute, value)
  VALUES('delete', old.id, old.entity, old.attribute, old.value);
END;

CREATE TRIGGER facts_fts_au AFTER UPDATE ON facts BEGIN
  INSERT INTO facts_fts(facts_fts, rowid, entity, attribute, value)
  VALUES('delete', old.id, old.entity, old.attribute, old.value);
  INSERT INTO facts_fts(rowid, entity, attribute, value)
  VALUES (new.id, new.entity, new.attribute, new.value);
END;
```

- [ ] **Step 4: Run all tests in the package**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1
```

Expected: PASS (three tests green).

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/sqlite/migrations/002_fts.sql pkg/memory/facts/sqlite/migrate_test.go
git commit -m "feat(memory/facts): FTS5 keyword index over facts"
```

---

## Task 5: SQLiteFactStore — Insert, GetByID, SoftDelete

**Files:**
- Create: `pkg/memory/facts/sqlite/store.go`
- Create: `pkg/memory/facts/sqlite/store_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/memory/facts/sqlite/store_test.go`:
```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	_ "modernc.org/sqlite"
)

func newStoreT(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func sampleFact() facts.Fact {
	now := time.Now().UTC()
	return facts.Fact{
		Namespace:    "tg:user:1",
		Entity:       "Андрей",
		Attribute:    "likes",
		Value:        "грейпфрут",
		Confidence:   0.8,
		SourceMsgRef: "msg-1",
		CreatedAt:    now,
		UpdatedAt:    now,
		LastSeenAt:   now,
	}
}

func TestStore_InsertAndGetByID(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()

	id, err := s.Insert(context.Background(), sampleFact())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == 0 {
		t.Fatal("zero id")
	}
	got, err := s.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Entity != "Андрей" || got.Value != "грейпфрут" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_GetByID_NotFound(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	_, err := s.GetByID(context.Background(), 999)
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_SoftDelete(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	if err := s.SoftDelete(context.Background(), id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	got, _ := s.GetByID(context.Background(), id)
	if got.DeletedAt.IsZero() {
		t.Fatal("DeletedAt still zero after SoftDelete")
	}
}
```

- [ ] **Step 2: Run — verify they fail**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1 -run TestStore
```

Expected: FAIL — `NewStore` and `Store` type missing.

- [ ] **Step 3: Implement Store with these three methods**

Create `pkg/memory/facts/sqlite/store.go`:
```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Store is a modernc.org/sqlite-backed facts.Store. Use NewStore.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Insert(ctx context.Context, f facts.Fact) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO facts
			(namespace, entity, attribute, value, confidence,
			 source_msg_ref, source_actor,
			 created_at, updated_at, last_seen_at,
			 access_count, ttl_seconds, embedding, embedding_norm)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.Namespace, f.Entity, f.Attribute, f.Value, f.Confidence,
		f.SourceMsgRef, nullableString(f.SourceActor),
		f.CreatedAt.UTC(), f.UpdatedAt.UTC(), f.LastSeenAt.UTC(),
		f.AccessCount, nullableInt(f.TTLSeconds),
		blobOrNil(f.Embedding), nullableFloat(f.EmbeddingNorm),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetByID(ctx context.Context, id int64) (facts.Fact, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, namespace, entity, attribute, value, confidence,
		       source_msg_ref, COALESCE(source_actor, ''),
		       created_at, updated_at, last_seen_at,
		       access_count, COALESCE(ttl_seconds, 0),
		       deleted_at, embedding, COALESCE(embedding_norm, 0)
		FROM facts WHERE id = ?`, id)
	return scanFact(row)
}

func (s *Store) SoftDelete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE facts SET deleted_at = ?, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return facts.ErrNotFound
	}
	return nil
}

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFact(r rowScanner) (facts.Fact, error) {
	var f facts.Fact
	var deletedAt sql.NullTime
	var embedding []byte
	err := r.Scan(
		&f.ID, &f.Namespace, &f.Entity, &f.Attribute, &f.Value, &f.Confidence,
		&f.SourceMsgRef, &f.SourceActor,
		&f.CreatedAt, &f.UpdatedAt, &f.LastSeenAt,
		&f.AccessCount, &f.TTLSeconds,
		&deletedAt, &embedding, &f.EmbeddingNorm,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return facts.Fact{}, facts.ErrNotFound
	}
	if err != nil {
		return facts.Fact{}, err
	}
	if deletedAt.Valid {
		f.DeletedAt = deletedAt.Time
	}
	if len(embedding) > 0 {
		f.Embedding = bytesToFloats(embedding)
	}
	return f, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func blobOrNil(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	return floatsToBytes(v)
}
```

- [ ] **Step 4: Add the byte/float conversion helpers**

Create `pkg/memory/facts/sqlite/blob.go`:
```go
package sqlite

import (
	"encoding/binary"
	"math"
)

// floatsToBytes packs a float32 slice into little-endian bytes.
func floatsToBytes(v []float32) []byte {
	out := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// bytesToFloats is the inverse of floatsToBytes.
func bytesToFloats(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}
```

- [ ] **Step 5: Run tests**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1
```

Expected: PASS (all tests so far green).

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/sqlite/store.go pkg/memory/facts/sqlite/blob.go pkg/memory/facts/sqlite/store_test.go
git commit -m "feat(memory/facts): SQLite store with Insert/GetByID/SoftDelete"
```

---

## Task 6: SQLiteFactStore — Update, FindByKey, ListByNamespace

**Files:**
- Modify: `pkg/memory/facts/sqlite/store.go`
- Modify: `pkg/memory/facts/sqlite/store_test.go`

- [ ] **Step 1: Add failing tests**

Append to `pkg/memory/facts/sqlite/store_test.go`:
```go
func TestStore_Update(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	got, _ := s.GetByID(context.Background(), id)
	got.Value = "арбуз"
	got.Confidence = 0.95
	got.UpdatedAt = time.Now().UTC()
	if err := s.Update(context.Background(), got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	back, _ := s.GetByID(context.Background(), id)
	if back.Value != "арбуз" || back.Confidence != 0.95 {
		t.Fatalf("Update did not persist: %+v", back)
	}
}

func TestStore_FindByKey(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	f, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if err != nil {
		t.Fatalf("FindByKey: %v", err)
	}
	if f.ID != id {
		t.Fatalf("got id %d want %d", f.ID, id)
	}
}

func TestStore_FindByKey_NotFound(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	_, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_FindByKey_SkipsDeleted(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	_ = s.SoftDelete(context.Background(), id)
	_, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound for deleted, got %v", err)
	}
}

func TestStore_ListByNamespace(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	for _, attr := range []string{"likes", "lives_in", "calls_self"} {
		f := sampleFact()
		f.Attribute = attr
		_, _ = s.Insert(context.Background(), f)
	}
	out, err := s.ListByNamespace(context.Background(), []string{"tg:user:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3, got %d", len(out))
	}
}
```

- [ ] **Step 2: Run — verify failure**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1 -run TestStore_Update
```

Expected: FAIL — methods missing.

- [ ] **Step 3: Implement Update, FindByKey, ListByNamespace**

Append to `pkg/memory/facts/sqlite/store.go`:
```go
func (s *Store) Update(ctx context.Context, f facts.Fact) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE facts SET
			value = ?, confidence = ?,
			updated_at = ?, last_seen_at = ?,
			access_count = ?, ttl_seconds = ?,
			deleted_at = ?, embedding = ?, embedding_norm = ?
		WHERE id = ?`,
		f.Value, f.Confidence,
		f.UpdatedAt.UTC(), f.LastSeenAt.UTC(),
		f.AccessCount, nullableInt(f.TTLSeconds),
		nullableTime(f.DeletedAt),
		blobOrNil(f.Embedding), nullableFloat(f.EmbeddingNorm),
		f.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return facts.ErrNotFound
	}
	return nil
}

func (s *Store) FindByKey(ctx context.Context, namespace, entity, attribute string) (facts.Fact, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, namespace, entity, attribute, value, confidence,
		       source_msg_ref, COALESCE(source_actor, ''),
		       created_at, updated_at, last_seen_at,
		       access_count, COALESCE(ttl_seconds, 0),
		       deleted_at, embedding, COALESCE(embedding_norm, 0)
		FROM facts
		WHERE namespace = ? AND entity = ? AND attribute = ? AND deleted_at IS NULL`,
		namespace, entity, attribute)
	return scanFact(row)
}

func (s *Store) ListByNamespace(ctx context.Context, namespaces []string) ([]facts.Fact, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	q, args := inClause(`
		SELECT id, namespace, entity, attribute, value, confidence,
		       source_msg_ref, COALESCE(source_actor, ''),
		       created_at, updated_at, last_seen_at,
		       access_count, COALESCE(ttl_seconds, 0),
		       deleted_at, embedding, COALESCE(embedding_norm, 0)
		FROM facts
		WHERE deleted_at IS NULL AND namespace IN (`,
		namespaces)
	rows, err := s.db.QueryContext(ctx, q+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []facts.Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func inClause(prefix string, vals []string) (string, []any) {
	if len(vals) == 0 {
		return prefix, nil
	}
	q := prefix
	args := make([]any, len(vals))
	for i, v := range vals {
		if i > 0 {
			q += ","
		}
		q += "?"
		args[i] = v
	}
	return q, args
}
```

- [ ] **Step 4: Run all tests**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/sqlite/
git commit -m "feat(memory/facts): SQLite Update/FindByKey/ListByNamespace"
```

---

## Task 7: SQLiteFactStore — kNN (brute-force, namespace-scoped)

**Files:**
- Create: `pkg/memory/facts/sqlite/knn.go`
- Modify: `pkg/memory/facts/sqlite/store_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/memory/facts/sqlite/store_test.go`:
```go
func TestStore_KNN(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	ctx := context.Background()

	// Two facts: one matches the query well, one doesn't.
	a := sampleFact()
	a.Attribute = "likes"
	a.Value = "грейпфрут"
	a.Embedding = []float32{1, 0, 0}
	a.EmbeddingNorm = 1.0
	idA, _ := s.Insert(ctx, a)

	b := sampleFact()
	b.Attribute = "lives_in"
	b.Value = "Минск"
	b.Embedding = []float32{0, 1, 0}
	b.EmbeddingNorm = 1.0
	_, _ = s.Insert(ctx, b)

	hits, err := s.KNN(ctx, []string{"tg:user:1"}, []float32{1, 0, 0}, 1.0, 1, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
	if hits[0].Fact.ID != idA {
		t.Fatalf("wrong hit: %+v", hits[0].Fact)
	}
	if hits[0].Score < 0.99 {
		t.Fatalf("expected near-1 score, got %.4f", hits[0].Score)
	}
}
```

- [ ] **Step 2: Run — verify failure**

Expected: FAIL — `KNN` not defined.

- [ ] **Step 3: Add shared cosine helpers in the facts package**

Create `pkg/memory/facts/cosine.go`:
```go
package facts

import "math"

// Cosine computes the cosine similarity of two equal-length float32 vectors,
// given their precomputed L2 norms. Returns 0 when either norm is zero or
// the lengths disagree.
func Cosine(a []float32, normA float64, b []float32, normB float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	denom := normA * normB
	if denom == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / denom
}

// Norm returns the L2 norm of v as a float64.
func Norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}
```

- [ ] **Step 4: Implement KNN using the shared helper**

Create `pkg/memory/facts/sqlite/knn.go`:
```go
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
```

- [ ] **Step 5: Run all tests**

Run:
```
go test ./pkg/memory/facts/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/cosine.go pkg/memory/facts/sqlite/knn.go pkg/memory/facts/sqlite/store_test.go
git commit -m "feat(memory/facts): cosine helpers + brute-force kNN scoped by namespace"
```

---

## Task 8: SQLiteFactStore — FTS5 KeywordSearch fallback

**Files:**
- Create: `pkg/memory/facts/sqlite/keyword.go`
- Modify: `pkg/memory/facts/sqlite/store_test.go`

- [ ] **Step 1: Add failing test**

Append to `pkg/memory/facts/sqlite/store_test.go`:
```go
func TestStore_KeywordSearch(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	ctx := context.Background()

	f := sampleFact()
	f.Value = "грейпфрутовый сок"
	_, _ = s.Insert(ctx, f)

	hits, err := s.KeywordSearch(ctx, []string{"tg:user:1"}, "грейпфрут", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
}
```

- [ ] **Step 2: Run — verify failure**

Expected: FAIL.

- [ ] **Step 3: Implement KeywordSearch**

Create `pkg/memory/facts/sqlite/keyword.go`:
```go
package sqlite

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

func (s *Store) KeywordSearch(ctx context.Context, namespaces []string, q string, k int) ([]facts.RecallHit, error) {
	if q == "" || k <= 0 || len(namespaces) == 0 {
		return nil, nil
	}
	query, args := inClause(`
		SELECT f.id, f.namespace, f.entity, f.attribute, f.value, f.confidence,
		       f.source_msg_ref, COALESCE(f.source_actor, ''),
		       f.created_at, f.updated_at, f.last_seen_at,
		       f.access_count, COALESCE(f.ttl_seconds, 0),
		       f.deleted_at, f.embedding, COALESCE(f.embedding_norm, 0),
		       bm25(facts_fts) AS rank
		FROM facts_fts
		JOIN facts f ON f.id = facts_fts.rowid
		WHERE facts_fts MATCH ?
		  AND f.deleted_at IS NULL
		  AND f.namespace IN (`,
		namespaces)
	args = append([]any{q}, args...)
	rows, err := s.db.QueryContext(ctx, query+`) ORDER BY rank LIMIT ?`, append(args, k)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []facts.RecallHit
	for rows.Next() {
		var rank float64
		var f facts.Fact
		var deletedAt interface{}
		var embedding []byte
		if err := rows.Scan(
			&f.ID, &f.Namespace, &f.Entity, &f.Attribute, &f.Value, &f.Confidence,
			&f.SourceMsgRef, &f.SourceActor,
			&f.CreatedAt, &f.UpdatedAt, &f.LastSeenAt,
			&f.AccessCount, &f.TTLSeconds,
			&deletedAt, &embedding, &f.EmbeddingNorm,
			&rank); err != nil {
			return nil, err
		}
		if len(embedding) > 0 {
			f.Embedding = bytesToFloats(embedding)
		}
		// bm25 returns negative; flip sign so larger is better, normalize loosely.
		score := -rank
		out = append(out, facts.RecallHit{Fact: f, Score: score})
	}
	return out, rows.Err()
}
```

(The first positional arg slot is the FTS match query. We rebuild args carefully because `inClause` only handles namespaces.)

- [ ] **Step 4: Run all tests**

Run:
```
go test ./pkg/memory/facts/sqlite/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/sqlite/keyword.go pkg/memory/facts/sqlite/store_test.go
git commit -m "feat(memory/facts): FTS5 KeywordSearch as embedding fallback"
```

---

## Task 9: Embedder interface + provider-routed impl

**Files:**
- Create: `pkg/memory/facts/embed/embedder.go`
- Create: `pkg/memory/facts/embed/embedder_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/memory/facts/embed/embedder_test.go`:
```go
package embed

import (
	"context"
	"testing"
)

type fakeProvider struct {
	called []string
	out    []float32
}

func (f *fakeProvider) Embed(ctx context.Context, model, text string) ([]float32, error) {
	f.called = append(f.called, model+"|"+text)
	return f.out, nil
}

func TestEmbedder_BasicCall(t *testing.T) {
	fp := &fakeProvider{out: []float32{0.1, 0.2, 0.3}}
	e := NewEmbedder(fp, "embed-slug", 3)
	v, n, err := e.Embed(context.Background(), "Андрей likes грейпфрут")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 {
		t.Fatalf("len mismatch: %d", len(v))
	}
	if n == 0 {
		t.Fatal("norm should be non-zero")
	}
	if len(fp.called) != 1 || fp.called[0] != "embed-slug|Андрей likes грейпфрут" {
		t.Fatalf("provider not called correctly: %v", fp.called)
	}
}

func TestEmbedder_DimMismatchAborts(t *testing.T) {
	fp := &fakeProvider{out: []float32{0.1, 0.2, 0.3, 0.4}}
	e := NewEmbedder(fp, "embed-slug", 3)
	_, _, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("want error on dim mismatch")
	}
}
```

- [ ] **Step 2: Run — verify failure**

Run:
```
go test ./pkg/memory/facts/embed/... -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement Embedder**

Create `pkg/memory/facts/embed/embedder.go`:
```go
package embed

import (
	"context"
	"fmt"
	"math"
)

// EmbeddingProvider is the minimal interface for an external embedding source.
// PicoClaw's provider routing implements this for OpenAI-compatible
// /embeddings endpoints.
type EmbeddingProvider interface {
	Embed(ctx context.Context, model, text string) ([]float32, error)
}

// Embedder wraps a provider with model-slug + dimension contract.
type Embedder struct {
	provider EmbeddingProvider
	modelSlug string
	dim       int
}

func NewEmbedder(p EmbeddingProvider, modelSlug string, dim int) *Embedder {
	return &Embedder{provider: p, modelSlug: modelSlug, dim: dim}
}

// Embed returns the vector and its L2 norm.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	v, err := e.provider.Embed(ctx, e.modelSlug, text)
	if err != nil {
		return nil, 0, err
	}
	if len(v) != e.dim {
		return nil, 0, fmt.Errorf("embed: dim mismatch: want %d got %d", e.dim, len(v))
	}
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return v, math.Sqrt(s), nil
}
```

- [ ] **Step 4: Run tests**

Run:
```
go test ./pkg/memory/facts/embed/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/embed/
git commit -m "feat(memory/facts): Embedder wrapper around provider routing"
```

---

## Task 10: Extractor prompt + parser

**Files:**
- Create: `pkg/memory/facts/extract/prompt.go`
- Create: `pkg/memory/facts/extract/parse.go`
- Create: `pkg/memory/facts/extract/parse_test.go`

- [ ] **Step 1: Failing test for the parser**

Create `pkg/memory/facts/extract/parse_test.go`:
```go
package extract

import (
	"testing"
)

func TestParseFacts_Valid(t *testing.T) {
	raw := `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.9}]}`
	out, err := ParseFacts(raw)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].Entity != "Андрей" {
		t.Fatalf("bad entity: %q", out[0].Entity)
	}
	if out[0].Confidence != 0.9 {
		t.Fatalf("bad confidence: %v", out[0].Confidence)
	}
}

func TestParseFacts_Malformed(t *testing.T) {
	if _, err := ParseFacts("not json"); err == nil {
		t.Fatal("want error on malformed json")
	}
}

func TestParseFacts_StripsFencing(t *testing.T) {
	raw := "```json\n{\"facts\":[]}\n```"
	out, err := ParseFacts(raw)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want 0, got %d", len(out))
	}
}
```

- [ ] **Step 2: Run — verify failure**

Run:
```
go test ./pkg/memory/facts/extract/... -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement parser**

Create `pkg/memory/facts/extract/parse.go`:
```go
package extract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractedFact is the wire shape returned by the extraction LLM.
type ExtractedFact struct {
	Entity     string  `json:"entity"`
	Attribute  string  `json:"attribute"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

type extractEnvelope struct {
	Facts []ExtractedFact `json:"facts"`
}

// ParseFacts extracts the structured facts array from an LLM response.
// It tolerates surrounding ```json fences but otherwise requires strict JSON.
func ParseFacts(raw string) ([]ExtractedFact, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var env extractEnvelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		return nil, fmt.Errorf("extract: parse: %w", err)
	}
	return env.Facts, nil
}
```

- [ ] **Step 4: Add the prompt builder**

Create `pkg/memory/facts/extract/prompt.go`:
```go
package extract

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// BuildPrompt renders the extraction system+user prompt for the given window
// of messages. The model is asked to reply with JSON only.
func BuildPrompt(window []providers.Message) (system, user string) {
	system = `Ты — извлекатель атомарных фактов из чата.

Правила:
- Извлекай только устойчивые факты о людях и чате (не сиюминутные настроения).
- Каждый факт — тройка (entity, attribute, value). Не извлекай предложений целиком.
- Уровень уверенности 0..1: 1 — прямое утверждение пользователем; 0.5 — выведено косвенно.
- Сохраняй язык значений как в исходнике (русский остаётся русским).
- Никаких лишних слов вокруг JSON. Отвечай ТОЛЬКО валидным JSON в формате:
{"facts":[{"entity":"...","attribute":"...","value":"...","confidence":0.0}]}
- Если фактов нет — верни {"facts":[]}.`

	var b strings.Builder
	for _, m := range window {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	user = "Сообщения:\n" + b.String() + "\nИзвлеки факты."
	return system, user
}
```

- [ ] **Step 5: Run tests**

Run:
```
go test ./pkg/memory/facts/extract/... -count=1
```

Expected: PASS (the parser tests; prompt builder has no tests yet — it's pure string formatting).

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/extract/
git commit -m "feat(memory/facts): extraction prompt + JSON parser"
```

---

## Task 11: Extractor async worker

**Files:**
- Create: `pkg/memory/facts/extract/worker.go`
- Create: `pkg/memory/facts/extract/worker_test.go`
- Create: `pkg/memory/facts/extract/log.go`

- [ ] **Step 1: Add `extraction_log` access helpers**

Create `pkg/memory/facts/extract/log.go`:
```go
package extract

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Logger reads and writes the extraction_log table.
type Logger struct{ DB *sql.DB }

func (l *Logger) LastProcessed(ctx context.Context, sessionKey string) (int, error) {
	row := l.DB.QueryRowContext(ctx,
		`SELECT last_processed_msg_idx FROM extraction_log WHERE session_key = ?`,
		sessionKey)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

func (l *Logger) Mark(ctx context.Context, sessionKey string, idx int) error {
	_, err := l.DB.ExecContext(ctx, `
		INSERT INTO extraction_log(session_key, last_processed_msg_idx, last_run_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			last_processed_msg_idx = excluded.last_processed_msg_idx,
			last_run_at = excluded.last_run_at`,
		sessionKey, idx, time.Now().UTC())
	return err
}

func (l *Logger) RecordFailure(ctx context.Context, sessionKey, msgRange, errMsg string) error {
	_, err := l.DB.ExecContext(ctx, `
		INSERT INTO failed_extractions(session_key, msg_range, error, failed_at)
		VALUES (?, ?, ?, ?)`,
		sessionKey, msgRange, errMsg, time.Now().UTC())
	return err
}
```

- [ ] **Step 2: Failing worker test**

Create `pkg/memory/facts/extract/worker_test.go`:
```go
package extract

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	"github.com/sipeed/picoclaw/pkg/providers"
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

func TestWorker_HappyPath(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	if err := factsqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := factsqlite.NewStore(db)
	logger := &Logger{DB: db}
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.9}]}`}

	w := NewWorker(store, &stubEmbed{}, llm, logger, "tg:user:1", "bot-1")
	w.Run(context.Background(), Job{
		SessionKey: "tg:user:1",
		Window: []providers.Message{
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
	_ = time.Now()
}
```

- [ ] **Step 3: Run — verify failure**

Run:
```
go test ./pkg/memory/facts/extract/... -count=1 -run TestWorker_HappyPath
```

Expected: FAIL — Worker not defined.

- [ ] **Step 4: Implement the worker**

Create `pkg/memory/facts/extract/worker.go`:
```go
package extract

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// Embedder is the minimal Embedder contract the worker depends on.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, float64, error)
}

// ExtractionLLM is the minimal LLM contract — one string in, one string out.
// Implementations route through pkg/providers using the configured slug.
type ExtractionLLM interface {
	ExtractFacts(ctx context.Context, system, user string) (string, error)
}

// Job is a unit of work for the extractor.
type Job struct {
	SessionKey string
	Window     []providers.Message
	StartIdx   int
	EndIdx     int
}

// Worker turns Jobs into persisted facts. One Worker per agent instance.
type Worker struct {
	store     facts.Store
	embed     Embedder
	llm       ExtractionLLM
	log       *Logger
	namespace string // primary namespace for inserted facts
	botRef    string // tg:bot:<botname> for source_msg_ref provenance
}

func NewWorker(store facts.Store, e Embedder, llm ExtractionLLM, log *Logger, namespace, botRef string) *Worker {
	return &Worker{store: store, embed: e, llm: llm, log: log, namespace: namespace, botRef: botRef}
}

// Run processes a single job synchronously. The caller drives async via a
// goroutine + buffered channel.
func (w *Worker) Run(ctx context.Context, j Job) {
	last, err := w.log.LastProcessed(ctx, j.SessionKey)
	if err == nil && j.EndIdx <= last {
		return
	}

	resp, err := w.callWithRetry(ctx, j.Window)
	if err != nil {
		_ = w.log.RecordFailure(ctx, j.SessionKey,
			fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		return
	}

	parsed, err := ParseFacts(resp)
	if err != nil {
		_ = w.log.RecordFailure(ctx, j.SessionKey,
			fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		return
	}

	for _, ef := range parsed {
		if err := w.persist(ctx, ef, j); err != nil {
			_ = w.log.RecordFailure(ctx, j.SessionKey,
				fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		}
	}

	_ = w.log.Mark(ctx, j.SessionKey, j.EndIdx)
}

func (w *Worker) callWithRetry(ctx context.Context, window []providers.Message) (string, error) {
	system, user := BuildPrompt(window)
	delays := []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second}
	var lastErr error
	for i, d := range append([]time.Duration{0}, delays...) {
		if d > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(d):
			}
		}
		out, err := w.llm.ExtractFacts(ctx, system, user)
		if err == nil {
			return out, nil
		}
		lastErr = err
		_ = i
	}
	if lastErr == nil {
		lastErr = errors.New("extract: empty after retries")
	}
	return "", lastErr
}
```

(The `persist` method is added in Task 12.)

- [ ] **Step 5: Stub out persist for the worker to compile**

Append to `pkg/memory/facts/extract/worker.go`:
```go
// persist applies dedupe/contradiction logic. Implemented in persist.go.
func (w *Worker) persist(ctx context.Context, ef ExtractedFact, j Job) error {
	return errors.New("persist: not implemented")
}
```

The test in step 2 will still fail until Task 12, so adjust expectations.

- [ ] **Step 6: Build the package (test will fail — that's fine)**

Run:
```
go build -tags "goolm,stdjson" ./pkg/memory/facts/extract/...
```

Expected: success.

- [ ] **Step 7: Commit**

```
git add pkg/memory/facts/extract/
git commit -m "feat(memory/facts): extractor worker scaffold + retry"
```

---

## Task 12: Extractor — persist (dedupe / contradiction / insert)

**Files:**
- Create: `pkg/memory/facts/extract/persist.go`
- Modify: `pkg/memory/facts/extract/worker.go` (drop stub)

- [ ] **Step 1: Add tests for the three branches**

Append to `pkg/memory/facts/extract/worker_test.go`:
```go
func TestWorker_DedupeRaisesConfidence(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)
	logger := &Logger{DB: db}
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.7}]}`}
	w := NewWorker(store, &stubEmbed{}, llm, logger, "tg:user:1", "bot")

	w.Run(context.Background(), Job{SessionKey: "tg:user:1", Window: nil, StartIdx: 0, EndIdx: 1})
	// Reset extraction log so the second call is not skipped.
	_ = logger.Mark(context.Background(), "tg:user:1", 0)
	w.Run(context.Background(), Job{SessionKey: "tg:user:1", Window: nil, StartIdx: 0, EndIdx: 2})

	f, _ := store.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if f.Confidence <= 0.7 {
		t.Fatalf("confidence should have been bumped, got %v", f.Confidence)
	}
}

func TestWorker_Contradiction(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)
	logger := &Logger{DB: db}
	llm := &fakeLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.7}]}`}
	w := NewWorker(store, &stubEmbed{}, llm, logger, "tg:user:1", "bot")
	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 1})
	_ = logger.Mark(context.Background(), "tg:user:1", 0)

	llm.resp = `{"facts":[{"entity":"Андрей","attribute":"likes","value":"арбуз","confidence":0.9}]}`
	w.Run(context.Background(), Job{SessionKey: "tg:user:1", EndIdx: 2})

	f, _ := store.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if f.Value != "арбуз" {
		t.Fatalf("contradiction not resolved, got %q", f.Value)
	}
}
```

- [ ] **Step 2: Run — verify failure**

Run:
```
go test ./pkg/memory/facts/extract/... -count=1
```

Expected: FAIL (current worker.persist returns `not implemented`).

- [ ] **Step 3: Replace the stub with real persistence**

In `worker.go` remove the stub `persist` method and instead create `pkg/memory/facts/extract/persist.go`:
```go
package extract

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Tunables.

const dedupeCosineThreshold = 0.9
const confidenceBumpStep = 0.05

func (w *Worker) persist(ctx context.Context, ef ExtractedFact, j Job) error {
	canonical := strings.ToLower(strings.TrimSpace(ef.Entity + " " + ef.Attribute + " " + ef.Value))
	vec, norm, err := w.embed.Embed(ctx, canonical)
	if err != nil {
		return err
	}

	existing, err := w.store.FindByKey(ctx, w.namespace, ef.Entity, ef.Attribute)
	now := time.Now().UTC()

	switch {
	case errors.Is(err, facts.ErrNotFound):
		_, err := w.store.Insert(ctx, facts.Fact{
			Namespace:    w.namespace,
			Entity:       ef.Entity,
			Attribute:    ef.Attribute,
			Value:        ef.Value,
			Confidence:   clamp01(ef.Confidence),
			SourceMsgRef: j.SessionKey,
			CreatedAt:    now,
			UpdatedAt:    now,
			LastSeenAt:   now,
			Embedding:    vec,
			EmbeddingNorm: norm,
		})
		return err

	case err != nil:
		return err

	default:
		// Existing live fact. Compute cosine of the new embedding vs the
		// stored one. If similar enough, treat as a duplicate (bump
		// confidence); otherwise treat as a contradiction.
		sim := 0.0
		if len(existing.Embedding) == len(vec) && existing.EmbeddingNorm > 0 {
			sim = facts.Cosine(vec, norm, existing.Embedding, existing.EmbeddingNorm)
		}
		if sim >= dedupeCosineThreshold {
			existing.Confidence = clamp01(existing.Confidence + confidenceBumpStep)
			existing.LastSeenAt = now
			existing.UpdatedAt = now
			existing.AccessCount++
			return w.store.Update(ctx, existing)
		}
		// Contradiction — soft-delete the old, insert the new.
		if err := w.store.SoftDelete(ctx, existing.ID); err != nil {
			return err
		}
		_, err := w.store.Insert(ctx, facts.Fact{
			Namespace:    w.namespace,
			Entity:       ef.Entity,
			Attribute:    ef.Attribute,
			Value:        ef.Value,
			Confidence:   clamp01(ef.Confidence),
			SourceMsgRef: j.SessionKey,
			CreatedAt:    now,
			UpdatedAt:    now,
			LastSeenAt:   now,
			Embedding:    vec,
			EmbeddingNorm: norm,
		})
		return err
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
```

Edit `pkg/memory/facts/extract/worker.go`: delete the stub `persist` method (the one added in Task 11 Step 5).

- [ ] **Step 4: Run all tests**

Run:
```
go test ./pkg/memory/facts/extract/... -count=1
```

Expected: PASS — happy path + dedupe + contradiction.

- [ ] **Step 5: Commit**

```
git add pkg/memory/facts/extract/
git commit -m "feat(memory/facts): persist with dedupe + contradiction resolution"
```

---

## Task 13: Recaller + render

**Files:**
- Create: `pkg/memory/facts/recall/recaller.go`
- Create: `pkg/memory/facts/recall/recaller_test.go`
- Create: `pkg/memory/facts/recall/render.go`
- Create: `pkg/memory/facts/recall/render_test.go`

- [ ] **Step 1: Failing render test**

Create `pkg/memory/facts/recall/render_test.go`:
```go
package recall

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

func TestRender_EmptyReturnsEmpty(t *testing.T) {
	if Render(nil) != "" {
		t.Fatal("want empty for nil")
	}
}

func TestRender_FormatsList(t *testing.T) {
	out := Render([]facts.RecallHit{
		{Fact: facts.Fact{Entity: "Андрей", Attribute: "likes", Value: "грейпфрут"}},
		{Fact: facts.Fact{Entity: "Андрей", Attribute: "lives_in", Value: "Минск"}},
	})
	if !strings.Contains(out, "Известно:") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "грейпфрут") {
		t.Fatalf("missing fact: %q", out)
	}
}
```

- [ ] **Step 2: Implement render**

Create `pkg/memory/facts/recall/render.go`:
```go
package recall

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Render formats recall hits into a compact Russian system-prefix block.
func Render(hits []facts.RecallHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Известно:\n")
	for _, h := range hits {
		b.WriteString("- ")
		b.WriteString(h.Fact.Entity)
		b.WriteString(" ")
		b.WriteString(h.Fact.Attribute)
		b.WriteString(": ")
		b.WriteString(h.Fact.Value)
		b.WriteString("\n")
	}
	return b.String()
}
```

- [ ] **Step 3: Failing recaller test**

Create `pkg/memory/facts/recall/recaller_test.go`:
```go
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

type stubEmbed struct{ v []float32; n float64 }

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
```

- [ ] **Step 4: Implement Recaller**

Create `pkg/memory/facts/recall/recaller.go`:
```go
package recall

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, float64, error)
}

// Recaller is the synchronous recall entry point used by channel handlers.
type Recaller struct {
	store    facts.Store
	embed    Embedder
	topK     int
	minScore float64
}

func NewRecaller(s facts.Store, e Embedder, topK int, minScore float64) *Recaller {
	return &Recaller{store: s, embed: e, topK: topK, minScore: minScore}
}

// Recall returns up to topK facts whose cosine to the embedded input is
// >= minScore, scoped by the given namespaces. Falls back to keyword search
// if embedding fails.
func (r *Recaller) Recall(ctx context.Context, namespaces []string, input string) ([]facts.RecallHit, error) {
	vec, norm, err := r.embed.Embed(ctx, input)
	if err != nil {
		return r.store.KeywordSearch(ctx, namespaces, input, r.topK)
	}
	return r.store.KNN(ctx, namespaces, vec, norm, r.topK, r.minScore)
}
```

- [ ] **Step 5: Run tests**

Run:
```
go test ./pkg/memory/facts/recall/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/recall/
git commit -m "feat(memory/facts): synchronous Recaller + Russian renderer"
```

---

## Task 14: Consolidator (decay + sweep)

**Files:**
- Create: `pkg/memory/facts/consolidate/consolidator.go`
- Create: `pkg/memory/facts/consolidate/consolidator_test.go`

- [ ] **Step 1: Failing test**

Create `pkg/memory/facts/consolidate/consolidator_test.go`:
```go
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

func TestConsolidate_DecaysOldFacts(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)

	old := time.Now().Add(-60 * 24 * time.Hour).UTC()
	id, _ := store.Insert(context.Background(), facts.Fact{
		Namespace: "tg:user:1", Entity: "X", Attribute: "Y", Value: "Z",
		Confidence: 0.4, SourceMsgRef: "m",
		CreatedAt: old, UpdatedAt: old, LastSeenAt: old,
	})

	c := NewConsolidator(store, Config{
		DecayWindow:   30 * 24 * time.Hour,
		MinConfidence: 0.25,
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
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()
	_ = factsqlite.Migrate(context.Background(), db)
	store := factsqlite.NewStore(db)
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
```

- [ ] **Step 2: Implement Consolidator**

Create `pkg/memory/facts/consolidate/consolidator.go`:
```go
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
```

- [ ] **Step 3: Run tests**

Run:
```
go test ./pkg/memory/facts/consolidate/... -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add pkg/memory/facts/consolidate/
git commit -m "feat(memory/facts): consolidator decays old facts"
```

---

## Task 15: Config struct + validation

**Files:**
- Modify: `pkg/config/<existing agent config file>` — add `Facts` block
- Create: `pkg/memory/facts/config.go`
- Create: `pkg/memory/facts/config_test.go`

- [ ] **Step 1: Locate the existing agent config struct**

Run:
```
grep -rn "memory" pkg/config | head -10
```

Expected: a struct or YAML key like `agents.defaults.memory` already exists from prior work. The new block lives next to it.

- [ ] **Step 2: Define the config type**

Create `pkg/memory/facts/config.go`:
```go
package facts

import (
	"fmt"
	"time"
)

type Config struct {
	Enabled               bool          `json:"enabled" yaml:"enabled"`
	ChannelScope          []string      `json:"channel_scope" yaml:"channel_scope"`
	ExtractionModel       string        `json:"extraction_model" yaml:"extraction_model"`
	EmbeddingModel        string        `json:"embedding_model" yaml:"embedding_model"`
	EmbeddingDim          int           `json:"embedding_dim" yaml:"embedding_dim"`
	TopK                  int           `json:"top_k" yaml:"top_k"`
	RecallMinScore        float64       `json:"recall_min_score" yaml:"recall_min_score"`
	ExtractionWindowMsgs  int           `json:"extraction_window_msgs" yaml:"extraction_window_msgs"`
	ExtractionIdleSeconds int           `json:"extraction_idle_seconds" yaml:"extraction_idle_seconds"`
	TTLDefaultDays        int           `json:"ttl_default_days" yaml:"ttl_default_days"`
	DecayWindowDays       int           `json:"decay_window_days" yaml:"decay_window_days"`
	MinConfidence         float64       `json:"min_confidence" yaml:"min_confidence"`
	SQLitePath            string        `json:"sqlite_path" yaml:"sqlite_path"`
}

// Validate checks that the config is internally consistent. Caller should
// also verify embedding_dim against the resolved embedding model after
// provider routing.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.ExtractionModel == "" || c.EmbeddingModel == "" {
		return fmt.Errorf("%w: extraction_model and embedding_model must be set", ErrInvalidConfig)
	}
	if c.EmbeddingDim <= 0 {
		return fmt.Errorf("%w: embedding_dim must be > 0", ErrInvalidConfig)
	}
	if c.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be > 0", ErrInvalidConfig)
	}
	if c.RecallMinScore < 0 || c.RecallMinScore > 1 {
		return fmt.Errorf("%w: recall_min_score must be in [0,1]", ErrInvalidConfig)
	}
	if c.SQLitePath == "" {
		return fmt.Errorf("%w: sqlite_path must be set", ErrInvalidConfig)
	}
	return nil
}

// DecayWindow returns DecayWindowDays as a time.Duration.
func (c Config) DecayWindow() time.Duration {
	return time.Duration(c.DecayWindowDays) * 24 * time.Hour
}
```

- [ ] **Step 3: Add tests**

Create `pkg/memory/facts/config_test.go`:
```go
package facts

import (
	"errors"
	"testing"
)

func TestConfig_DisabledIsValid(t *testing.T) {
	if err := (Config{Enabled: false}).Validate(); err != nil {
		t.Fatalf("disabled config should validate: %v", err)
	}
}

func TestConfig_RequiresFields(t *testing.T) {
	err := (Config{Enabled: true}).Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}
}

func TestConfig_HappyPath(t *testing.T) {
	c := Config{
		Enabled:         true,
		ExtractionModel: "x",
		EmbeddingModel:  "y",
		EmbeddingDim:    1536,
		TopK:            8,
		RecallMinScore:  0.55,
		SQLitePath:      "/tmp/facts.db",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("happy: %v", err)
	}
}
```

- [ ] **Step 4: Run tests**

Run:
```
go test ./pkg/memory/facts/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Wire into the project's existing config loader**

Find where `agents.defaults` is unmarshaled. Add a `Memory.Facts` field (or `Facts` directly under memory, matching the existing structure). Confirm `agents.memory.facts.enabled=false` is the default by leaving the block off the example config.

This step requires reading the existing config file; do exactly the minimal addition needed to expose `facts.Config` to startup code. Confirm by running:
```
go build -tags "goolm,stdjson" ./...
```

Expected: success.

- [ ] **Step 6: Commit**

```
git add pkg/memory/facts/config.go pkg/memory/facts/config_test.go pkg/config/
git commit -m "feat(memory/facts): config struct + validation hooked into agents config"
```

---

## Task 16: Telegram channel hook

**Files:**
- Create: `pkg/channels/telegram/memory_hook.go`
- Create: `pkg/channels/telegram/memory_hook_test.go`
- Modify: `pkg/channels/telegram/<the inbound message handler>`

- [ ] **Step 1: Find the handler entry point**

Run:
```
grep -rn "func.*MessageUpdate\|func.*OnMessage\|HandleUpdate" pkg/channels/telegram/*.go | head -10
```

Note the file and function that owns inbound text-message processing.

- [ ] **Step 2: Write the hook test**

Create `pkg/channels/telegram/memory_hook_test.go`:
```go
package telegram

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

type fakeRecaller struct {
	called bool
	out    []facts.RecallHit
}

func (f *fakeRecaller) Recall(ctx context.Context, ns []string, input string) ([]facts.RecallHit, error) {
	f.called = true
	return f.out, nil
}

type fakeQueue struct{ enqueued int }

func (f *fakeQueue) Enqueue(j ExtractionJob) { f.enqueued++ }

func TestMemoryHook_InjectsAndEnqueues(t *testing.T) {
	r := &fakeRecaller{
		out: []facts.RecallHit{{Fact: facts.Fact{
			Entity: "Андрей", Attribute: "likes", Value: "грейпфрут",
		}}},
	}
	q := &fakeQueue{}
	hook := NewMemoryHook(r, q)

	prefix, err := hook.PreAgent(context.Background(), MemoryInputs{
		ChatID: -100, UserID: 1, BotUsername: "c0md_bot", Input: "что любит Андрей?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prefix == "" {
		t.Fatal("expected non-empty prefix")
	}
	if !r.called {
		t.Fatal("recaller not called")
	}

	hook.PostAgent(context.Background(), MemoryInputs{
		ChatID: -100, UserID: 1, BotUsername: "c0md_bot",
	}, ExtractionJob{SessionKey: "tg:user:1", EndIdx: 1})
	if q.enqueued != 1 {
		t.Fatalf("enqueue count: %d", q.enqueued)
	}
}
```

- [ ] **Step 3: Implement the hook**

Create `pkg/channels/telegram/memory_hook.go`:
```go
package telegram

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
)

// ExtractionJob is the data the hook hands to the extraction worker. The
// concrete worker lives in pkg/memory/facts/extract; this type is mirrored
// here to keep telegram free of imports it shouldn't have.
type ExtractionJob struct {
	SessionKey string
	StartIdx   int
	EndIdx     int
}

// Recaller is the subset of recall.Recaller the hook depends on.
type Recaller interface {
	Recall(ctx context.Context, namespaces []string, input string) ([]facts.RecallHit, error)
}

// ExtractionQueue is the subset of the extractor the hook depends on.
type ExtractionQueue interface {
	Enqueue(j ExtractionJob)
}

// MemoryInputs is what the telegram handler passes to the hook.
type MemoryInputs struct {
	ChatID      int64
	UserID      int64
	BotUsername string
	Input       string
}

type MemoryHook struct {
	rec   Recaller
	queue ExtractionQueue
}

func NewMemoryHook(rec Recaller, queue ExtractionQueue) *MemoryHook {
	return &MemoryHook{rec: rec, queue: queue}
}

// PreAgent embeds the user input, runs kNN over the chat + user + bot
// namespaces, and returns a system-prefix string to prepend to the agent's
// system prompt. Empty string when there are no relevant facts.
func (h *MemoryHook) PreAgent(ctx context.Context, in MemoryInputs) (string, error) {
	if h == nil || h.rec == nil {
		return "", nil
	}
	ns := []string{
		fmt.Sprintf("%s%d", facts.NSPrefixTGChat, in.ChatID),
		fmt.Sprintf("%s%d", facts.NSPrefixTGUser, in.UserID),
		facts.NSPrefixTGBot + in.BotUsername,
	}
	hits, err := h.rec.Recall(ctx, ns, in.Input)
	if err != nil {
		return "", err
	}
	return recall.Render(hits), nil
}

// PostAgent enqueues an extraction job for the recent message window. The
// hook never blocks the user-facing turn.
func (h *MemoryHook) PostAgent(ctx context.Context, in MemoryInputs, j ExtractionJob) {
	if h == nil || h.queue == nil {
		return
	}
	h.queue.Enqueue(j)
}
```

- [ ] **Step 4: Wire into the inbound handler**

In the file located by Step 1, locate the point where the agent is invoked. Wrap that invocation:
- Before: call `MemoryHook.PreAgent(...)`, prepend the returned prefix to the agent's system text.
- After: call `MemoryHook.PostAgent(...)` with the message-window bounds the agent just consumed.

Keep the change minimal — only the two calls and the prefix prepend. The hook is nil-safe; the wiring should accept nil when the config flag is off.

- [ ] **Step 5: Run channel tests**

Run:
```
go test ./pkg/channels/telegram/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add pkg/channels/telegram/
git commit -m "feat(telegram): memory hook for recall + extraction"
```

---

## Task 17: Wire startup (instantiate, migrate, register cron)

**Files:**
- Modify: wherever agents are constructed in `pkg/agent` or `cmd/picoclaw`
- Create: `pkg/memory/facts/bootstrap.go`

- [ ] **Step 1: Locate the agent-construction site**

Run:
```
grep -rn "func NewAgent\|agent.New\|memory.NewStore" pkg/agent cmd/picoclaw | head -10
```

Note where the existing memory store is instantiated. The facts subsystem hangs off the same construction site.

- [ ] **Step 2: Add a bootstrap helper**

Create `pkg/memory/facts/bootstrap.go`:
```go
package facts

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/memory/facts/consolidate"
	"github.com/sipeed/picoclaw/pkg/memory/facts/embed"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	_ "modernc.org/sqlite"
)

// Subsystem holds every long-lived piece the agent needs to use facts memory.
type Subsystem struct {
	Store        Store
	Recaller     *recall.Recaller
	Worker       *extract.Worker
	Consolidator *consolidate.Consolidator
	Logger       *extract.Logger
	Close        func() error
}

// Bootstrap instantiates the facts subsystem from config. Returns a no-op
// Subsystem when cfg.Enabled is false.
func Bootstrap(ctx context.Context, cfg Config, prov embed.EmbeddingProvider, llm extract.ExtractionLLM, botUsername string) (*Subsystem, error) {
	if !cfg.Enabled {
		return &Subsystem{Close: func() error { return nil }}, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("facts: open sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		return nil, fmt.Errorf("facts: pragmas: %w", err)
	}
	if err := factsqlite.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("facts: migrate: %w", err)
	}

	store := factsqlite.NewStore(db)
	embedder := embed.NewEmbedder(prov, cfg.EmbeddingModel, cfg.EmbeddingDim)
	recaller := recall.NewRecaller(store, embedder, cfg.TopK, cfg.RecallMinScore)
	logger := &extract.Logger{DB: db}
	worker := extract.NewWorker(store, embedder, llm, logger, "", "tg:bot:"+botUsername)
	cons := consolidate.NewConsolidator(store, consolidate.Config{
		DecayWindow:   cfg.DecayWindow(),
		MinConfidence: cfg.MinConfidence,
	})

	return &Subsystem{
		Store:        store,
		Recaller:     recaller,
		Worker:       worker,
		Consolidator: cons,
		Logger:       logger,
		Close: func() error {
			return store.Close()
		},
	}, nil
}
```

- [ ] **Step 3: Wire `Bootstrap` at the agent construction site**

In the file found in Step 1, after the existing `pkg/memory` store is created, call `facts.Bootstrap(...)` with:
- `cfg` from the loaded config (`agents.memory.facts`)
- a provider implementing `embed.EmbeddingProvider` (a thin adapter over the existing routing — add it in `pkg/providers/embed_adapter.go` if needed; spec §11 risk #3 already calls this out)
- a provider implementing `extract.ExtractionLLM` (uses `cfg.ExtractionModel`)
- `botUsername` from telegram config

Then pass the resulting `Subsystem` to the telegram channel constructor, which uses it to build a `MemoryHook` (nil-safe when subsystem is disabled).

- [ ] **Step 4: Register the consolidator with cron**

In the same wiring code, after `Bootstrap`:
```go
if subsystem.Consolidator != nil {
    cronSvc.Register("memory.facts.decay", "0 4 * * *", func(ctx context.Context) error {
        return subsystem.Consolidator.Run(ctx, nil)  // nil = all namespaces
    })
}
```

Adjust to the existing `pkg/cron` registration API; the function name and signature are guesses — verify by reading `pkg/cron/service.go`.

For `Run(ctx, nil)` to mean "all namespaces", extend `Consolidator.Run` to accept `nil` as a "scan all" sentinel. If the existing code uses an explicit list, instead enumerate namespaces from the store with a new `Store.DistinctNamespaces` method — add the method, the test for it, and the migration if needed.

- [ ] **Step 5: Build the full project**

Run:
```
go build -tags "goolm,stdjson" ./...
```

Expected: success.

- [ ] **Step 6: Run all tests**

Run:
```
go test -tags "goolm,stdjson" ./... -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add .
git commit -m "feat(memory/facts): wire bootstrap + cron registration into agent startup"
```

---

## Task 18: End-to-end integration test

**Files:**
- Create: `pkg/memory/facts/integration_test.go`

- [ ] **Step 1: Write the integration test**

Create `pkg/memory/facts/integration_test.go`:
```go
package facts_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/memory/facts/embed"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	factsqlite "github.com/sipeed/picoclaw/pkg/memory/facts/sqlite"
	"github.com/sipeed/picoclaw/pkg/providers"
	_ "modernc.org/sqlite"
)

type scriptedLLM struct{ resp string }

func (s *scriptedLLM) ExtractFacts(ctx context.Context, sys, user string) (string, error) {
	return s.resp, nil
}

type scriptedEmbed struct{}

func (s *scriptedEmbed) Embed(ctx context.Context, model, text string) ([]float32, error) {
	// Hash text into a deterministic 8-dim vector for the test only.
	v := make([]float32, 8)
	for i, r := range text {
		v[i%8] += float32(r%7) / 7
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
	embedder := embed.NewEmbedder(&scriptedEmbed{}, "stub", 8)
	llm := &scriptedLLM{resp: `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрутовый сок","confidence":0.9}]}`}
	logger := &extract.Logger{DB: db}
	worker := extract.NewWorker(store, embedder, llm, logger, "tg:user:42", "tg:bot:c0md_bot")

	// Round 1: extract from a scripted conversation.
	worker.Run(context.Background(), extract.Job{
		SessionKey: "tg:user:42",
		Window: []providers.Message{
			{Role: "user", Content: "Андрей сказал что любит грейпфрутовый сок"},
		},
		StartIdx: 0, EndIdx: 1,
	})

	// Round 2: recall in a "later" turn.
	r := recall.NewRecaller(store, &embedderAdapter{e: embedder}, 5, 0.0)
	hits, err := r.Recall(context.Background(), []string{"tg:user:42"}, "что любит Андрей?")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if hits[0].Fact.Value != "грейпфрутовый сок" {
		t.Fatalf("wrong fact: %q", hits[0].Fact.Value)
	}
	_ = time.Now()
	_ = facts.NSPrefixTGUser
}

// embedderAdapter exposes embed.Embedder under the Recaller's Embedder
// interface (different package shape).
type embedderAdapter struct{ e *embed.Embedder }

func (a *embedderAdapter) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return a.e.Embed(ctx, text)
}
```

- [ ] **Step 2: Run the integration test**

Run:
```
go test ./pkg/memory/facts/ -count=1 -run TestE2E
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add pkg/memory/facts/integration_test.go
git commit -m "test(memory/facts): end-to-end extract → recall flow"
```

---

## Task 19: Deploy + smoke test on rpi3

**Files:**
- (No source changes — runbook only.)

- [ ] **Step 1: Cross-build for rpi3**

Run on Windows (worktree root):
```
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags "goolm,stdjson" -ldflags "-s -w" -o build/picoclaw-linux-arm64 ./cmd/picoclaw
```

Expected: `build/picoclaw-linux-arm64` exists.

- [ ] **Step 2: Push branch to fork remote**

Run:
```
git push fork feat/telegram-memory
```

- [ ] **Step 3: Sync code on rpi3 deploy repo**

Run:
```
ssh andy@172.30.0.3 'cd /home/andy/picoclaw-deploy && git fetch origin && git checkout feat/telegram-memory && git reset --hard origin/feat/telegram-memory'
```

(Note: the rpi3 deploy repo's remote is named `origin` for the fork.)

- [ ] **Step 4: scp the binary and install**

Run:
```
scp build/picoclaw-linux-arm64 andy@172.30.0.3:/tmp/picoclaw.new
ssh andy@172.30.0.3 'sudo install -m 755 /tmp/picoclaw.new /usr/local/bin/picoclaw && rm /tmp/picoclaw.new'
```

- [ ] **Step 5: Enable facts memory in launcher config**

Edit `/home/andy/.picoclaw/config.json` on rpi3 to add:
```json
"agents": {
  "memory": {
    "facts": {
      "enabled": true,
      "channel_scope": ["telegram"],
      "extraction_model": "<existing model_list slug>",
      "embedding_model": "<embedding model_list slug>",
      "embedding_dim": 1536,
      "top_k": 8,
      "recall_min_score": 0.55,
      "extraction_window_msgs": 20,
      "extraction_idle_seconds": 30,
      "ttl_default_days": 90,
      "decay_window_days": 30,
      "min_confidence": 0.2,
      "sqlite_path": "/home/andy/.picoclaw/facts.db"
    }
  }
}
```

The exact `model_list` slugs depend on what the user has configured. If `/embeddings` is not yet wired, add a `model_list` entry with the right `api_base`.

- [ ] **Step 6: Restart gateway via launcher**

Run:
```
ssh andy@172.30.0.3 'TOKEN=$(tr "\0" "\n" < /proc/$(pgrep -f picoclaw-launch)/environ | grep ^PICOCLAW_LAUNCHER_TOKEN= | cut -d= -f2-); curl -sf -X POST -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18800/api/gateway/restart && sleep 4 && curl -sf http://127.0.0.1:18790/health'
```

Expected: `{"pid":...,"status":"ok"}` then `{"status":"ok","uptime":"..."}`.

- [ ] **Step 7: Run the smoke conversation**

Manually (in Telegram):
1. Send to Коробъка: "Запомни: я люблю грейпфрутовый сок."
2. Wait ~60 seconds (extraction worker idle window).
3. Confirm extraction:
   ```
   ssh andy@172.30.0.3 'sqlite3 /home/andy/.picoclaw/facts.db "SELECT namespace, entity, attribute, value, confidence FROM facts WHERE deleted_at IS NULL"'
   ```
   Expect a row mentioning "грейпфрутовый сок".
4. In a fresh session (or after `/clear`), ask: "что я люблю?"
5. Verify the bot's reply mentions грейпфрутовый сок.

- [ ] **Step 8: Document the smoke result**

Append a short report to `docs/design/2026-05-20-telegram-memory-design.md` under a new section §15 "Smoke test 2026-XX-XX" — pass/fail per checklist item, log snippets, any tuning made to `recall_min_score` or `top_k`.

- [ ] **Step 9: Commit + push**

```
git add docs/design/2026-05-20-telegram-memory-design.md
git commit -m "docs(memory): rpi3 smoke test results"
git push fork feat/telegram-memory
```

---

## Self-review checklist

After completing all tasks:
- All spec sections (§1–§14) have at least one task implementing them.
- No "TBD" or "fill in later" anywhere in this plan or in the code it produced.
- `go test -tags "goolm,stdjson" ./... -count=1` green.
- `go build -tags "goolm,stdjson" ./...` green.
- The feature flag `agents.memory.facts.enabled=false` leaves all behavior identical to today's bot.
- Roll-back path documented in spec §12 still works (flip flag + restart).
