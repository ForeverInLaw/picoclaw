package seahorse_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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

func TestCanonicalAppendKeepsBoundedImportPlansAndRetainsLegacyRevisionKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	var sources []seahorse.Message
	for i := range 16 {
		sources = append(sources, seahorse.Message{Role: "user", Content: fmt.Sprint("original ", i), TokenCount: 10})
		if err = engine.ReconcileCanonical(t.Context(), "generation", sources, ""); err != nil {
			t.Fatal(err)
		}
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var plans int
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports").Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if plans > 2 {
		t.Fatalf("ordinary appends retained full historical plans: %d", plans)
	}
	retained := append([]seahorse.Message(nil), sources[len(sources)-1:]...)
	if err = engine.ReconcileCanonical(t.Context(), "generation", retained, "legacy rewrite"); err != nil {
		t.Fatal(err)
	}
	for i := range 16 {
		retained = append(retained, seahorse.Message{Role: "user", Content: fmt.Sprint("new ", i), TokenCount: 10})
		if err = engine.ReconcileCanonical(t.Context(), "generation", retained, "legacy rewrite"); err != nil {
			t.Fatal(err)
		}
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports").Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if plans > 3 {
		t.Fatalf("appends accumulated plans after a legacy rewrite: %d", plans)
	}
	// The original shorter fingerprint was pruned, but its source identities
	// remain in the retained revision boundary and must not be duplicated.
	if err = engine.ReconcileCanonical(t.Context(), "generation", sources[:4], ""); err != nil {
		t.Fatal(err)
	}
	var persisted int
	if err = database.QueryRow("SELECT COUNT(*) FROM source_messages").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 32 {
		t.Fatalf("pruned fingerprint lost source identities: %d", persisted)
	}
}

func TestCanonicalReconciliationRestoresKnownSourcesInOrderWithoutDuplicatingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	sources := []seahorse.Message{
		{Role: "user", Content: "first original", TokenCount: 10},
		{Role: "assistant", Content: "retained original", TokenCount: 10},
	}
	for _, state := range []struct {
		messages []seahorse.Message
		summary  string
	}{
		{sources, ""},
		{sources[1:], "legacy truncation"},
		{sources, ""},
	} {
		if err = engine.ReconcileCanonical(t.Context(), "generation", state.messages, state.summary); err != nil {
			t.Fatal(err)
		}
	}
	assembled, err := engine.Assemble(t.Context(), "generation", seahorse.AssembleInput{Budget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Messages) != 2 || assembled.Messages[0].Content != sources[0].Content || assembled.Messages[1].Content != sources[1].Content {
		t.Fatalf("canonical restoration lost order or sources: %+v", assembled.Messages)
	}
	if strings.Contains(assembled.Summary, "legacy truncation") {
		t.Fatal("replaced canonical summary remained active")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var persisted int
	if err = database.QueryRow("SELECT COUNT(*) FROM source_messages").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 2 {
		t.Fatal("canonical restoration duplicated evidence")
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
