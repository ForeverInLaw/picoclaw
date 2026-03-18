package memoryindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
