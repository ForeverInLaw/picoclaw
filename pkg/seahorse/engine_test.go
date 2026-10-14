package seahorse_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/seahorse"
)

func TestForcedCompactionRetainsCompleteSourceGraphThroughCondensationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: path}, func(context.Context, string, seahorse.CompleteOptions) (string, error) { return "brief summary", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	source := make([]seahorse.Message, 64)
	for i := range source {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		source[i] = seahorse.Message{Role: role, Content: fmt.Sprint(i) + strings.Repeat("evidence ", 1200), TokenCount: 4500}
	}
	if err := engine.SyncSources(t.Context(), "agent:main:current", source); err != nil {
		t.Fatal(err)
	}
	result, err := engine.CompactUntilUnder(t.Context(), "agent:main:current", 25)
	if err != nil {
		t.Fatal(err)
	}
	if result.CondensedSummaries == 0 {
		t.Fatal("forced compaction did not condense summaries")
	}
	engine.Close()
	engine, err = seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	store := engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(t.Context(), "agent:main:current")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.GetContextItems(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	visited := map[string]bool{}
	var collect func(string)
	collect = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		summary, e := store.GetSummary(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		if summary.EarliestAt != nil || summary.LatestAt != nil {
			t.Fatal("unknown source times became precise summary timestamps")
		}
		messages, e := store.GetSummarySourceMessages(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		for _, msg := range messages {
			seen[msg.ID] = true
		}
		parents, e := store.GetSummaryParents(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		for _, parent := range parents {
			collect(parent.SummaryID)
		}
	}
	for _, item := range items {
		if item.ItemType == "summary" {
			collect(item.SummaryID)
		} else {
			seen[item.MessageID] = true
		}
	}
	if len(seen) != len(source) {
		t.Fatalf("source graph lost evidence: %d of %d", len(seen), len(source))
	}
	if err = engine.SyncSources(t.Context(), "agent:main:current", source); err != nil {
		t.Fatal(err)
	}
	found, err := engine.GetRetrieval().Grep(t.Context(), seahorse.GrepInput{ConversationID: conv.ConversationID, Pattern: "%evidence%", Scope: "message", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if found.TotalMessages != len(source) {
		t.Fatalf("reopen duplicated canonical sources: %d", found.TotalMessages)
	}
}

func TestGenerationFenceRejectsLateCompactionPublicationWithoutHoldingLockDuringCompletion(t *testing.T) {
	var gate sync.Mutex
	current := true
	guard := func(commit func() error) (bool, error) {
		gate.Lock()
		defer gate.Unlock()
		if !current {
			return false, nil
		}
		return true, commit()
	}
	started := make(chan struct{})
	release := make(chan struct{})
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")}, func(context.Context, string, seahorse.CompleteOptions) (string, error) {
		close(started)
		<-release
		return "late summary", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx := seahorse.WithCommitGuard(t.Context(), guard)
	source := make([]seahorse.Message, 8)
	for i := range source {
		source[i] = seahorse.Message{Role: "user", Content: strings.Repeat("evidence ", 1000), TokenCount: 4000}
	}
	if err = engine.SyncSources(ctx, "captured-generation", source); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := engine.CompactUntilUnder(ctx, "captured-generation", 1000); done <- e }()
	<-started
	// A reset can acquire the publication gate while the provider is blocked.
	gate.Lock()
	current = false
	gate.Unlock()
	close(release)
	if err = <-done; err == nil {
		t.Fatal("late summary was published after reset")
	}
	store := engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(t.Context(), "captured-generation")
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := store.GetSummariesByConversation(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatal("stale summary was persisted")
	}
	if err = engine.SyncSources(ctx, "captured-generation", append(source, seahorse.Message{Role: "user", Content: "late source"})); err == nil {
		t.Fatal("late source was accepted")
	}
	items, err := store.GetContextItems(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 8 {
		t.Fatal("stale operation changed active context")
	}
}
