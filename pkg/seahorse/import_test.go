package seahorse_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/seahorse"
)

func TestCanonicalImportCheckpointRollsBackPublicationAndResumesAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`CREATE TRIGGER interrupt_checkpoint BEFORE INSERT ON context_checkpoints BEGIN SELECT RAISE(FAIL,'checkpoint interrupted'); END`); err != nil {
		t.Fatal(err)
	}
	sources := []seahorse.Message{{Role: "user", Content: "original evidence", TokenCount: 10}}
	if err = engine.ReconcileCanonical(t.Context(), "generation", sources, "legacy summary"); err == nil {
		t.Fatal("interrupted checkpoint reported success")
	}
	var persisted, summaries, completed int
	if err = database.QueryRow("SELECT COUNT(*) FROM source_messages").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM summaries").Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 || summaries != 0 || completed != 0 {
		t.Fatalf("interrupted publication: sources=%d summaries=%d complete=%d", persisted, summaries, completed)
	}
	engine.Close()
	if _, err = database.Exec("DROP TRIGGER interrupt_checkpoint"); err != nil {
		t.Fatal(err)
	}
	engine, err = seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for range 2 {
		if err = engine.ReconcileCanonical(t.Context(), "generation", sources, "legacy summary"); err != nil {
			t.Fatal(err)
		}
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM source_messages").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM summaries WHERE kind='imported' AND earliest_at IS NULL AND latest_at IS NULL").Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 || summaries != 1 || completed != 1 {
		t.Fatalf("resumed publication: sources=%d summaries=%d complete=%d", persisted, summaries, completed)
	}
}

func TestEngineClearResetsCanonicalImportCheckpoint(t *testing.T) {
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for range 2 {
		if err = engine.ReconcileCanonical(t.Context(), "generation", nil, "summary-only evidence"); err != nil {
			t.Fatal(err)
		}
		store := engine.GetRetrieval().Store()
		conv, err := store.GetConversationBySessionKey(t.Context(), "generation")
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.GetContextItems(t.Context(), conv.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ItemType != "summary" {
			t.Fatal("clear retained an import checkpoint without its summary")
		}
		if err = engine.ClearSession(t.Context(), "generation"); err != nil {
			t.Fatal(err)
		}
	}
}
