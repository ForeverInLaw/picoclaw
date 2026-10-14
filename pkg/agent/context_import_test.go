package agent

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestSeahorseLazilyImportsSummaryOnlyConversationAndReopens(t *testing.T) {
	p := &sourceScenarioProvider{}
	al, cfg := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	agent.Sessions.SetSummary(key, "LEGACY_SUMMARY_ONLY bicycle evidence")
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "first", "continue")); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(p.lastInput)
	if !strings.Contains(input, `kind="imported"`) || !strings.Contains(input, "LEGACY_SUMMARY_ONLY") {
		t.Fatalf("legacy summary lacks imported provenance: %s", input)
	}
	if agent.Sessions.GetSummary(key) != "LEGACY_SUMMARY_ONLY bicycle evidence" {
		t.Fatal("import changed canonical summary")
	}
	al.Close()
	p = &sourceScenarioProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Close()
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "reopened", "continue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(p.lastInput), "LEGACY_SUMMARY_ONLY") {
		t.Fatal("reopen lost imported summary")
	}
	database, err := sql.Open("sqlite", filepath.Join(agent.Workspace, "sessions", "seahorse-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var summaries, complete int
	if err = database.QueryRow("SELECT COUNT(*) FROM summaries WHERE kind='imported'").Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if summaries != 1 || complete == 0 {
		t.Fatalf("durable import: summaries=%d complete=%d", summaries, complete)
	}
}

func TestSeahorseInterruptedImportReopensWithoutFalseCompletionOrDuplicateSources(t *testing.T) {
	provider := &sourceScenarioProvider{}
	al, cfg := newSeahorseScenarioLoop(t, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	original := []providers.Message{
		{Role: "user", Content: "persisted first source", SourceID: "original-1"},
		{
			Role:         "assistant",
			Content:      "fail import source",
			ContentPhase: providers.ContentPhaseCommentary,
			SourceID:     "original-2",
			ModelName:    "historical-model",
			Media:        []string{"media://original"},
		},
	}
	for _, msg := range original {
		agent.Sessions.AddFullMessage(key, msg)
	}
	agent.Sessions.SetSummary(key, "LEGACY_IMPORT_SUMMARY")
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(agent.Workspace, "sessions", strings.ReplaceAll(key, ":", "_")+".jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(agent.Workspace, "sessions", "seahorse-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(
		`CREATE TRIGGER interrupt_import BEFORE INSERT ON messages WHEN NEW.content='fail import source' BEGIN SELECT RAISE(FAIL,'interrupt import'); END`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = al.processMessage(t.Context(), selectedInlineMessage("42", "failure", "continue")); err == nil {
		t.Fatal("interrupted import reported success")
	}
	if len(provider.lastInput) != 0 {
		t.Fatal("provider received partial migration")
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed import changed canonical source")
	}
	var sources, completed int
	if err = database.QueryRow("SELECT COUNT(*) FROM source_messages").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || completed != 0 {
		t.Fatalf("partial import: sources=%d completed=%d", sources, completed)
	}
	al.Close()
	if _, err = database.Exec("DROP TRIGGER interrupt_import"); err != nil {
		t.Fatal(err)
	}
	provider = &sourceScenarioProvider{
		tool: "short_grep",
		args: map[string]any{"pattern": "%source%", "scope": "message"},
	}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	if _, err = al.processMessage(t.Context(), selectedInlineMessage("42", "resume", "query sources")); err != nil {
		t.Fatal(err)
	}
	assertExpandedSources(t, provider.expandOutput, original)
	if err = database.QueryRow("SELECT COUNT(*) FROM messages WHERE content IN ('persisted first source','fail import source')").
		Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != 2 {
		t.Fatalf("resumption duplicated sources: %d", sources)
	}
}

func TestSeahorseReconcilesLegacyTruncationAndPreservesCurrentGenerationDAG(t *testing.T) {
	provider := &sourceScenarioProvider{}
	al, cfg := newSeahorseScenarioLoop(t, provider)
	key := inlineConversationKey("main", "42")
	for _, content := range []string{"ORIGINAL_OLD_PREFIX", "RETAINED_TAIL"} {
		if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "seahorse", content)); err != nil {
			t.Fatal(err)
		}
	}
	al.Close()
	cfg.Agents.Defaults.ContextManager = "legacy"
	provider = &sourceScenarioProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	agent := al.GetRegistry().GetDefaultAgent()
	if len(agent.Sessions.GetHistory(key)) != 4 {
		t.Fatal("explicit legacy lost Seahorse canonical turns")
	}
	agent.Sessions.TruncateHistory(key, 2)
	agent.Sessions.SetSummary(key, "LEGACY_SUMMARY_AFTER_TRUNCATION")
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "legacy", "FRESH_LEGACY_SUFFIX"),
	); err != nil {
		t.Fatal(err)
	}
	al.Close()
	cfg.Agents.Defaults.ContextManager = "seahorse"
	provider = &sourceScenarioProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "returned", "continue")); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(provider.lastInput)
	for _, want := range []string{"RETAINED_TAIL", "FRESH_LEGACY_SUFFIX", "LEGACY_SUMMARY_AFTER_TRUNCATION"} {
		if !strings.Contains(input, want) {
			t.Errorf("reconciliation lost %s", want)
		}
	}
	if strings.Contains(input, "ORIGINAL_OLD_PREFIX") {
		t.Fatal("legacy-truncated raw message returned automatically")
	}
	database, err := sql.Open("sqlite", filepath.Join(agent.Workspace, "sessions", "seahorse-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var oldSources int
	if err = database.QueryRow("SELECT COUNT(*) FROM messages WHERE content='ORIGINAL_OLD_PREFIX'").
		Scan(&oldSources); err != nil {
		t.Fatal(err)
	}
	if oldSources != 1 {
		t.Fatal("reconciliation erased or duplicated retained source DAG")
	}
}

func TestSeahorseStaleCapturedSnapshotCannotReconcileNewerCanonicalProjection(t *testing.T) {
	provider := &sourceScenarioProvider{}
	al, _ := newSeahorseScenarioLoop(t, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	agent.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "earlier source"})
	captured, _, err := agent.Sessions.(*session.ConversationStore).CaptureTurn(key)
	if err != nil {
		t.Fatal(err)
	}
	guard := captured.(session.ContextTurn)
	state, err := guard.ReadContext(key)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := state.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	old := ContextSnapshot{
		SessionKey:           key,
		ScopeKey:             agent.ID + "\x00" + guard.ConversationKey(key),
		Guard:                guard,
		History:              state.History,
		Summary:              state.Summary,
		CanonicalFingerprint: fingerprint,
		Provider:             agent.ContextProvider,
		Model:                agent.Model,
		Options:              map[string]any{"max_tokens": 256, "temperature": 0.7},
	}
	if err = agent.ContextManager.Ingest(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "NEWER_CANONICAL_SOURCE"})
	state, err = guard.ReadContext(key)
	if err != nil {
		t.Fatal(err)
	}
	newer := old
	newer.History = state.History
	newer.CanonicalFingerprint, err = state.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.ContextManager.Ingest(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	if err = agent.ContextManager.Ingest(t.Context(), old); !errors.Is(err, session.ErrContextChanged) {
		t.Fatalf("stale snapshot accepted: %v", err)
	}
	if _, err = al.processMessage(t.Context(), selectedInlineMessage("42", "current", "continue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastInput), "NEWER_CANONICAL_SOURCE") {
		t.Fatal("stale background snapshot removed newer source projection")
	}
}

func TestSeahorseCanonicalReadFailureDoesNotCompleteMigrationOrCallProvider(t *testing.T) {
	provider := &sourceScenarioProvider{}
	al, _ := newSeahorseScenarioLoop(t, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	agent.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "must retain source"})
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(agent.Workspace, "sessions", strings.ReplaceAll(key, ":", "_")+".meta.json")
	if err := os.WriteFile(meta, []byte("corrupt canonical metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "unreadable", "continue")); err == nil {
		t.Fatal("unreadable canonical source silently became empty")
	}
	if len(provider.lastInput) != 0 {
		t.Fatal("provider called after failed migration")
	}
	database, err := sql.Open("sqlite", filepath.Join(agent.Workspace, "sessions", "seahorse-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var completed int
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatal("read failure published completion")
	}
}
