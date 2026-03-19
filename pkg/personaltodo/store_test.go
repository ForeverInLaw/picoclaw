package personaltodo

import (
	"path/filepath"
	"testing"
)

func TestStore_CRUDAndPersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "personal_todos.sqlite")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	item1, err := store.Add(t.Context(), "telegram:42", "buy milk", "telegram", "42")
	if err != nil {
		t.Fatalf("Add(item1) error: %v", err)
	}
	if item1.ItemID != 1 {
		t.Fatalf("item1.ItemID = %d, want 1", item1.ItemID)
	}

	item2, err := store.Add(t.Context(), "telegram:42", "check server", "telegram", "-1001")
	if err != nil {
		t.Fatalf("Add(item2) error: %v", err)
	}
	if item2.ItemID != 2 {
		t.Fatalf("item2.ItemID = %d, want 2", item2.ItemID)
	}

	if _, err := store.MarkDone(t.Context(), "telegram:42", 1); err != nil {
		t.Fatalf("MarkDone() error: %v", err)
	}
	if _, err := store.Reopen(t.Context(), "telegram:42", 1); err != nil {
		t.Fatalf("Reopen() error: %v", err)
	}
	if _, err := store.MarkDone(t.Context(), "telegram:42", 1); err != nil {
		t.Fatalf("MarkDone(again) error: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	store, err = Open(dbPath)
	if err != nil {
		t.Fatalf("Open(reopen) error: %v", err)
	}
	defer store.Close()

	counts, err := store.Counts(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("Counts() error: %v", err)
	}
	if counts.Total != 2 || counts.Done != 1 || counts.Open != 1 {
		t.Fatalf("Counts() = %#v, want Total=2 Done=1 Open=1", counts)
	}

	items, err := store.List(t.Context(), "telegram:42", "", 10)
	if err != nil {
		t.Fatalf("List(all) error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List(all) len = %d, want 2", len(items))
	}

	cleared, err := store.ClearCompleted(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("ClearCompleted() error: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("ClearCompleted() = %d, want 1", cleared)
	}

	remaining, err := store.List(t.Context(), "telegram:42", "", 10)
	if err != nil {
		t.Fatalf("List(remaining) error: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ItemID != 2 {
		t.Fatalf("remaining items = %#v, want only item #2", remaining)
	}

	if err := store.Delete(t.Context(), "telegram:42", 2); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	finalCounts, err := store.Counts(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("Counts(final) error: %v", err)
	}
	if finalCounts.Total != 0 {
		t.Fatalf("Counts(final).Total = %d, want 0", finalCounts.Total)
	}
}

func TestStore_IsolatesOwnersAndKeepsPerOwnerCounters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "personal_todos.sqlite"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()

	alice1, err := store.Add(t.Context(), "telegram:42", "alice one", "telegram", "42")
	if err != nil {
		t.Fatalf("Add(alice1) error: %v", err)
	}
	bob1, err := store.Add(t.Context(), "telegram:99", "bob one", "telegram", "99")
	if err != nil {
		t.Fatalf("Add(bob1) error: %v", err)
	}
	alice2, err := store.Add(t.Context(), "telegram:42", "alice two", "telegram", "-1001")
	if err != nil {
		t.Fatalf("Add(alice2) error: %v", err)
	}

	if alice1.ItemID != 1 || alice2.ItemID != 2 || bob1.ItemID != 1 {
		t.Fatalf("unexpected per-owner counters: alice1=%d alice2=%d bob1=%d", alice1.ItemID, alice2.ItemID, bob1.ItemID)
	}

	aliceItems, err := store.List(t.Context(), "telegram:42", "", 10)
	if err != nil {
		t.Fatalf("List(alice) error: %v", err)
	}
	if len(aliceItems) != 2 {
		t.Fatalf("List(alice) len = %d, want 2", len(aliceItems))
	}

	bobItems, err := store.List(t.Context(), "telegram:99", "", 10)
	if err != nil {
		t.Fatalf("List(bob) error: %v", err)
	}
	if len(bobItems) != 1 || bobItems[0].Text != "bob one" {
		t.Fatalf("List(bob) = %#v, want only bob's item", bobItems)
	}
}
