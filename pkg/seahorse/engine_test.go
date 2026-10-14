package seahorse_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestForcedCompactionRetainsCompleteSourceGraphThroughCondensationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(
		seahorse.Config{DBPath: path},
		func(context.Context, string, seahorse.CompleteOptions) (string, error) { return "brief summary", nil },
	)
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
		source[i] = seahorse.Message{
			Role:       role,
			Content:    fmt.Sprint(i) + strings.Repeat("evidence ", 1200),
			TokenCount: 4500,
		}
	}
	if err = engine.SyncSources(t.Context(), "agent:main:current", source); err != nil {
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
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.ItemType == "summary" {
			ids = append(ids, item.SummaryID)
		} else {
			ids = append(ids, fmt.Sprintf("msg:%d", item.MessageID))
		}
	}
	expanded, err := engine.GetRetrieval().ExpandSources(t.Context(), "agent:main:current", append(ids, ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Messages) != len(source) {
		t.Fatalf("bounded retrieval lost or duplicated originals: %d of %d", len(expanded.Messages), len(source))
	}
	if err = engine.SyncSources(
		t.Context(),
		"agent:main:foreign",
		[]seahorse.Message{{Role: "user", Content: "separate"}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.GetRetrieval().ExpandSources(t.Context(), "agent:main:foreign", ids); err == nil {
		t.Fatal("retrieval accepted another conversation's source graph")
	}
	if err = engine.ReconcileCanonical(t.Context(), "agent:main:current", source, ""); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.GetContextItems(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != len(items) {
		t.Fatal("canonical import rebuilt the existing compacted projection")
	}
	for i, item := range items {
		if reconciled[i].SummaryID != item.SummaryID || reconciled[i].MessageID != item.MessageID {
			t.Fatal("canonical import replaced existing DAG references")
		}
	}
	found, err := engine.GetRetrieval().
		Grep(t.Context(), seahorse.GrepInput{ConversationID: conv.ConversationID, Pattern: "%evidence%", Scope: "message", Limit: 100})
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
	engine, err := seahorse.NewEngine(
		seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")},
		func(context.Context, string, seahorse.CompleteOptions) (string, error) {
			close(started)
			<-release
			return "late summary", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx := seahorse.WithCommitGuard(t.Context(), guard)
	source := make([]seahorse.Message, 8, 9)
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
	if err = engine.SyncSources(
		ctx,
		"captured-generation",
		append(source, seahorse.Message{Role: "user", Content: "late source"}),
	); err == nil {
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

func TestCanceledIngestionReleasesGenerationGateBeforeSQLiteWriterUnlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	boundaryDir := t.TempDir()
	canonical := session.NewConversationStore(session.NewSessionManager(boundaryDir), boundaryDir)
	defer canonical.Close()
	captured, _, err := canonical.CaptureTurn("conversation")
	if err != nil {
		t.Fatal(err)
	}
	guard := captured.(session.ConversationTurn)
	scope := guard.ConversationKey("conversation")
	if err = engine.SyncSources(t.Context(), scope, nil); err != nil {
		t.Fatal(err)
	}
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	connection, err := lockDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err = connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(t.Context(), "ROLLBACK")
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = seahorse.WithCommitGuard(ctx, func(commit func() error) (bool, error) {
		return guard.CommitIfCurrent(func() error { close(entered); return commit() })
	})
	done := make(chan error, 1)
	go func() {
		done <- engine.SyncSources(ctx, scope, []seahorse.Message{{Role: "user", Content: "stale blocked source", TokenCount: 10}})
	}()
	<-entered
	cancel()
	reset := make(chan error, 1)
	go func() { reset <- canonical.ResetConversation("conversation") }()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled ingest reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled ingest remained blocked on SQLite writer")
	}
	select {
	case err = <-reset:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled ingest held reset gate")
	}
	if _, err = connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	conv, err := engine.GetRetrieval().Store().GetConversationBySessionKey(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	found, err := engine.GetRetrieval().
		Grep(t.Context(), seahorse.GrepInput{ConversationID: conv.ConversationID, Pattern: "%stale%", Scope: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if found.TotalMessages != 0 {
		t.Fatal("canceled ingestion persisted old source")
	}
	if current, err := guard.IsCurrent(); err != nil || current {
		t.Fatal("reset did not advance the captured generation")
	}
}

func TestGuardedEngineClearCannotDeleteStaleGenerationAndResetsSourceIdentity(t *testing.T) {
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err = engine.SyncSources(
		t.Context(),
		"generation",
		[]seahorse.Message{{Role: "user", Content: "old evidence", TokenCount: 10}},
	); err != nil {
		t.Fatal(err)
	}
	stale := seahorse.WithCommitGuard(t.Context(), func(func() error) (bool, error) { return false, nil })
	if err = engine.ClearSession(stale, "generation"); err == nil {
		t.Fatal("stale clear was accepted")
	}
	if err = engine.ClearSession(t.Context(), "generation"); err != nil {
		t.Fatal(err)
	}
	if err = engine.SyncSources(
		t.Context(),
		"generation",
		[]seahorse.Message{{Role: "user", Content: "replacement evidence", TokenCount: 10}},
	); err != nil {
		t.Fatal(err)
	}
	store := engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(t.Context(), "generation")
	if err != nil {
		t.Fatal(err)
	}
	found, err := engine.GetRetrieval().
		Grep(t.Context(), seahorse.GrepInput{ConversationID: conv.ConversationID, Pattern: "%evidence%", Scope: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Messages) != 1 || !strings.Contains(found.Messages[0].Snippet, "replacement") {
		t.Fatal("clear retained a stale source identity")
	}
}
