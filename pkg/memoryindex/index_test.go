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
