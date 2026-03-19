package memoryindex

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIndex_AddObservationAndSearch(t *testing.T) {
	idx, err := Open(filepath.Join(t.TempDir(), "memory", "index.sqlite"), Config{
		MaxResults:      3,
		MaxSnippetChars: 100,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	err = idx.AddObservation(context.Background(), Observation{
		SessionKey: "agent:main:telegram:direct:1",
		Channel:    "telegram",
		ChatID:     "1",
		Role:       "user",
		SenderID:   "telegram:1",
		Content:    "обсудили memory index и sqlite поиск",
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("AddObservation() error: %v", err)
	}

	hits, err := idx.Search(context.Background(), SearchRequest{
		Query:      "sqlite поиск",
		SessionKey: "agent:main:telegram:direct:1",
		Channel:    "telegram",
		ChatID:     "1",
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if !strings.Contains(hits[0].Content, "sqlite") {
		t.Fatalf("unexpected hit content: %q", hits[0].Content)
	}
}

func TestIndex_BootstrapSessionsImportsExistingHistory(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	meta := `{"key":"agent:main:telegram:direct:42"}`
	if err := os.WriteFile(filepath.Join(sessionsDir, "agent_main_telegram_direct_42.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error: %v", err)
	}

	jsonl := `{"role":"user","content":"мы уже обсуждали векторную память"}` + "\n" +
		`{"role":"assistant","content":"да, лучше начать с sqlite fts"}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, "agent_main_telegram_direct_42.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error: %v", err)
	}

	idx, err := Open(filepath.Join(root, "memory", "index.sqlite"), Config{
		MaxResults:      5,
		MaxSnippetChars: 120,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if err := idx.BootstrapSessions(context.Background(), sessionsDir); err != nil {
		t.Fatalf("BootstrapSessions() error: %v", err)
	}

	hits, err := idx.Search(context.Background(), SearchRequest{
		Query:      "векторную память",
		SessionKey: "agent:main:telegram:direct:42",
		Channel:    "telegram",
		ChatID:     "42",
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected bootstrapped hit")
	}
}

func TestIndex_BootstrapWorkspaceFilesImportsDurableMemory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".learnings"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.learnings) error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatalf("MkdirAll(memory) error: %v", err)
	}

	memoryDoc := "# Long-term Memory\n\n## Preferences\n\nUser prefers SQLite-first infrastructure decisions.\n"
	if err := os.WriteFile(filepath.Join(workspace, "memory", "MEMORY.md"), []byte(memoryDoc), 0o644); err != nil {
		t.Fatalf("WriteFile(MEMORY.md) error: %v", err)
	}

	learningDoc := "# Learnings\n\n## [LRN-20260318-001] insight\n\n### Summary\nHybrid search can wait until lexical retrieval is insufficient.\n\n### Details\nStart with SQLite FTS5 because it is simpler to operate locally.\n"
	if err := os.WriteFile(filepath.Join(workspace, ".learnings", "LEARNINGS.md"), []byte(learningDoc), 0o644); err != nil {
		t.Fatalf("WriteFile(LEARNINGS.md) error: %v", err)
	}

	idx, err := Open(filepath.Join(root, "memory", "index.sqlite"), Config{
		MaxResults:      5,
		MaxSnippetChars: 200,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if err := idx.BootstrapWorkspaceFiles(context.Background(), workspace); err != nil {
		t.Fatalf("BootstrapWorkspaceFiles() error: %v", err)
	}

	hits, err := idx.Search(context.Background(), SearchRequest{Query: "SQLite-first infrastructure decisions"})
	if err != nil {
		t.Fatalf("Search(memory doc) error: %v", err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Content, "SQLite-first infrastructure decisions") {
		t.Fatalf("expected workspace memory hit, got %#v", hits)
	}

	hits, err = idx.Search(context.Background(), SearchRequest{Query: "SQLite FTS5"})
	if err != nil {
		t.Fatalf("Search(learning doc) error: %v", err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Content, "SQLite FTS5") {
		t.Fatalf("expected learning hit, got %#v", hits)
	}
}

func TestIndex_SyncWorkspaceFiles_ReplacesUpdatedDocumentContent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatalf("MkdirAll(memory) error: %v", err)
	}

	docPath := filepath.Join(workspace, "memory", "MEMORY.md")
	initial := "# Long-term Memory\n\n## Preferences\n\nALPHA-SHARD-91.\n"
	if err := os.WriteFile(docPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("WriteFile(initial) error: %v", err)
	}

	idx, err := Open(filepath.Join(root, "memory", "index.sqlite"), Config{
		MaxResults:      5,
		MaxSnippetChars: 200,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if err := idx.SyncWorkspaceFiles(context.Background(), workspace); err != nil {
		t.Fatalf("SyncWorkspaceFiles(initial) error: %v", err)
	}

	updated := "# Long-term Memory\n\n## Preferences\n\nOMEGA-TRACE-27.\n"
	if err := os.WriteFile(docPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("WriteFile(updated) error: %v", err)
	}

	if err := idx.SyncWorkspaceFiles(context.Background(), workspace); err != nil {
		t.Fatalf("SyncWorkspaceFiles(updated) error: %v", err)
	}

	oldHits, err := idx.Search(context.Background(), SearchRequest{Query: "ALPHA-SHARD-91"})
	if err != nil {
		t.Fatalf("Search(old) error: %v", err)
	}
	if len(oldHits) != 0 {
		t.Fatalf("expected old content to be removed, got %#v", oldHits)
	}

	newHits, err := idx.Search(context.Background(), SearchRequest{Query: "OMEGA-TRACE-27"})
	if err != nil {
		t.Fatalf("Search(new) error: %v", err)
	}
	if len(newHits) == 0 || !strings.Contains(newHits[0].Content, "OMEGA-TRACE-27") {
		t.Fatalf("expected updated content hit, got %#v", newHits)
	}
}

func TestIndex_Open_RepairsLegacySchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory", "index.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	db, err := sql.Open(sqliteDriver, dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			role TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL
		);`,
		`CREATE INDEX idx_observations_session ON observations(session_key);`,
		`INSERT INTO observations(session_key, channel, chat_id, role, sender_id, content, created_at_ms)
		 VALUES ('agent:main:telegram:group:-1001', 'telegram', '-1001', 'user', 'telegram:42', 'legacy row', 1234567890);`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy schema setup failed for %q: %v", stmt, err)
		}
	}
	_ = db.Close()

	idx, err := Open(dbPath, Config{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if got := idx.SchemaStatus(); got != schemaStatusRepaired {
		t.Fatalf("SchemaStatus() = %q, want %q", got, schemaStatusRepaired)
	}

	checkDB, err := sql.Open(sqliteDriver, dbPath)
	if err != nil {
		t.Fatalf("sql.Open(check) error: %v", err)
	}
	defer checkDB.Close()

	columnNames := map[string]bool{}
	rows, err := checkDB.Query(`PRAGMA table_info(observations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(observations) error: %v", err)
	}
	for rows.Next() {
		var (
			cid      int
			name     string
			typ      string
			notNull  int
			defaultV sql.NullString
			pk       int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &pk); err != nil {
			t.Fatalf("Scan(table_info) error: %v", err)
		}
		columnNames[name] = true
	}
	rows.Close()

	for _, column := range []string{"peer_kind", "chat_label", "source_kind", "source_key"} {
		if !columnNames[column] {
			t.Fatalf("expected repaired observations.%s column, columns=%v", column, columnNames)
		}
	}

	for _, table := range []string{"chat_catalog", "chat_aliases", "chat_participants", "chat_rollups"} {
		var exists int
		if err := checkDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
			t.Fatalf("table exists check for %s failed: %v", table, err)
		}
		if exists != 1 {
			t.Fatalf("expected repaired table %s to exist", table)
		}
	}

	var legacyCount int
	if err := checkDB.QueryRow(`SELECT COUNT(*) FROM observations WHERE content = 'legacy row'`).Scan(&legacyCount); err != nil {
		t.Fatalf("legacy row count failed: %v", err)
	}
	if legacyCount != 1 {
		t.Fatalf("legacy row count = %d, want 1", legacyCount)
	}
}
