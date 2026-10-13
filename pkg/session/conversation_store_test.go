package session

import "testing"

func TestConversationStorePersistentBoundaryRemainsAuthoritativeAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first := NewConversationStore(NewSessionManager(dir), dir)
	first.AddMessage("chat", "user", "OLD_DIALOG")
	captured, _, err := first.CaptureTurn("chat")
	if err != nil {
		t.Fatal(err)
	}
	second := NewConversationStore(NewSessionManager(dir), dir)
	if err = second.ResetConversation("chat"); err != nil {
		t.Fatal(err)
	}
	captured.AddMessage("chat", "assistant", "LATE_OLD_RESULT")
	fresh, resetAt, err := first.CaptureTurn("chat")
	if err != nil || resetAt.IsZero() || len(fresh.GetHistory("chat")) != 0 {
		t.Fatalf("external durable reset was hidden by cached state: reset=%v err=%v", resetAt, err)
	}
	fresh.AddMessage("chat", "user", "FRESH_DIALOG")
	if history := first.GetHistory("chat"); len(history) != 1 || history[0].Content != "FRESH_DIALOG" {
		t.Fatalf("active history=%+v", history)
	}
}

func TestConversationStoreMemoryOnlyResetIsAuthoritative(t *testing.T) {
	store := NewConversationStore(NewSessionManager(""), "")
	old, _, err := store.CaptureTurn("chat")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ResetConversation("chat"); err != nil {
		t.Fatal(err)
	}
	old.AddMessage("chat", "user", "old")
	store.AddMessage("chat", "user", "new")
	if history := store.GetHistory("chat"); len(history) != 1 || history[0].Content != "new" {
		t.Fatalf("memory-only boundary=%+v", history)
	}
}
