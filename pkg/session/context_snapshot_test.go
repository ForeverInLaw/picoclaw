package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/session"
)

func TestCanonicalSnapshotPreservesLegacyBackendReadFailureAndResetBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte("malformed legacy JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := session.NewConversationStore(session.NewSessionManager(dir), dir)
	defer store.Close()
	captured, _, err := store.CaptureTurn("current")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = captured.(session.ContextReader).ReadContext("current"); err == nil {
		t.Fatal("unreadable current legacy snapshot became empty")
	}
	if err = store.ResetConversation("current"); err != nil {
		t.Fatal(err)
	}
	captured, _, err = store.CaptureTurn("current")
	if err != nil {
		t.Fatal(err)
	}
	state, err := captured.(session.ContextReader).ReadContext("current")
	if err != nil || len(state.History) != 0 || state.Summary != "" {
		t.Fatal("archived read failure leaked into new generation")
	}
}
