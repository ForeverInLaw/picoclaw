package sherlock

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_RecordMessage_PrunesToTwenty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state", "sherlock.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for i := 1; i <= 25; i++ {
		if err := store.RecordMessage(ctx, "42", "alice", "Alice", fmt.Sprintf("message %02d", i)); err != nil {
			t.Fatalf("RecordMessage(%d) error = %v", i, err)
		}
	}

	messages, err := store.GetMessages(ctx, "42", 25)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(messages) != 20 {
		t.Fatalf("len(messages) = %d, want 20", len(messages))
	}
	if messages[0].Text != "message 25" {
		t.Fatalf("latest message = %q, want message 25", messages[0].Text)
	}
	if messages[len(messages)-1].Text != "message 06" {
		t.Fatalf("oldest retained message = %q, want message 06", messages[len(messages)-1].Text)
	}

	user, ok, err := store.GetUser(ctx, "42")
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	if !ok {
		t.Fatal("expected user to exist")
	}
	if user.MessageCount != 25 {
		t.Fatalf("user.MessageCount = %d, want 25", user.MessageCount)
	}
}

func TestStore_ListUsers_OrdersByLastSeen(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state", "sherlock.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.RecordMessage(ctx, "1", "alice", "Alice", "first"); err != nil {
		t.Fatalf("RecordMessage(alice) error = %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.RecordMessage(ctx, "2", "bob", "Bob", "second"); err != nil {
		t.Fatalf("RecordMessage(bob) error = %v", err)
	}

	users, total, err := store.ListUsers(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(users) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(users))
	}
	if users[0].UserID != "2" || users[1].UserID != "1" {
		t.Fatalf("unexpected order: %#v", users)
	}
}
