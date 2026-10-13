package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestConversationResetSurvivesRestartAndRetainsOnlySavedFacts(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.GetRegistry().GetDefaultAgent()
	// Enable archive retrieval independently: reset must also hold when it is
	// enabled, although the deployed symptom does not require retrieval at all.
	al.Close()
	cfg.Agents.Defaults.MemoryIndex = config.MemoryIndexConfig{Enabled: true, MaxResults: 5, MaxSnippetChars: 500, MinQueryChars: 3}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	agent = al.GetRegistry().GetDefaultAgent()
	if err := agent.ContextBuilder.memory.WriteLongTerm("# Saved facts\n\nMy bicycle is blue.\n\n# Temporary notes\n\nOLD_MEMORY_NOTE"); err != nil {
		t.Fatal(err)
	}
	if err := agent.ContextBuilder.memory.AppendToday("OLD_DAILY_NOTE"); err != nil {
		t.Fatal(err)
	}
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Sender: bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42"}, Peer: bus.Peer{Kind: "direct", ID: "42"}, Content: "OLD_DIALOG bicycle"}
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	route, _, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		t.Fatal(routeErr)
	}
	key := route.SessionKey
	if err := agent.MemoryIndex.AddObservation(t.Context(), memoryindex.Observation{SessionKey: key, Channel: "telegram", ChatID: "42", PeerKind: "direct", Role: "user", SenderID: "telegram:42", Content: "OLD_ARCHIVE bicycle", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	agent.Sessions.SetSummary(key, "OLD_SUMMARY")
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	msg.Content = "/clear"
	response, err := al.processMessage(t.Context(), msg)
	if err != nil || !strings.Contains(response, "cleared") {
		t.Fatalf("reset = %q, %v", response, err)
	}
	al.Close()
	provider := &recordingProvider{}
	reopened := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer reopened.Close()
	msg.Content = "What do you know about my bicycle?"
	if _, err := reopened.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(provider.lastMessages)
	for _, old := range []string{"OLD_DIALOG", "OLD_SUMMARY", "OLD_MEMORY_NOTE", "OLD_DAILY_NOTE", "OLD_ARCHIVE"} {
		if strings.Contains(input, old) {
			t.Errorf("reset context contains %s", old)
		}
	}
	if !strings.Contains(input, "My bicycle is blue.") {
		t.Fatalf("saved fact missing from context: %s", input)
	}
	// A reset archives the previous generation; it must not delete its transcript.
	files, err := filepath.Glob(filepath.Join(agent.Workspace, "sessions", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		found = found || strings.Contains(string(data), "OLD_DIALOG")
	}
	if !found {
		t.Fatal("archived dialog was deleted")
	}
	archive := &archiveRecallProvider{}
	reopened.GetRegistry().GetDefaultAgent().Provider = archive
	msg.Content = "Search the archived dialog for the earlier bicycle discussion"
	if _, err := reopened.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(archive.messages), "OLD_ARCHIVE bicycle") {
		t.Fatal("explicit archive request could not retrieve retained history")
	}
}

type archiveRecallProvider struct {
	messages []providers.Message
	calls    int
}

func (p *archiveRecallProvider) GetDefaultModel() string { return "mock-model" }

func (p *archiveRecallProvider) Chat(_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.calls++
	p.messages = append([]providers.Message(nil), messages...)
	if p.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{ID: "archive-call", Name: "chat_memory", Arguments: map[string]any{"mode": "search", "chat": "current", "query": "bicycle"}}}}, nil
	}
	return &providers.LLMResponse{Content: "Retrieved archived discussion"}, nil
}

func TestConversationResetReportsPersistenceFailureAndRetainsConversation(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Peer: bus.Peer{Kind: "direct", ID: "42"}, Content: "keep this conversation"}
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	agent := al.GetRegistry().GetDefaultAgent()
	if err := os.WriteFile(filepath.Join(agent.Workspace, "sessions", "boundaries"), []byte("unwritable directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg.Content = "/clear"
	response, err := al.processMessage(t.Context(), msg)
	if err != nil || !strings.Contains(response, "Failed to clear") {
		t.Fatalf("failed reset = %q, %v", response, err)
	}
	provider := &recordingProvider{}
	agent.Provider = provider
	msg.Content = "follow up"
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "keep this conversation") {
		t.Fatal("failed reset lost active history")
	}
}

func TestConversationResetCannotBeRepopulatedByActiveTurn(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	provider := &resetRaceProvider{started: make(chan struct{}), release: make(chan struct{})}
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Provider = provider
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Peer: bus.Peer{Kind: "direct", ID: "42"}, Content: "OLD_IN_FLIGHT"}
	finished := make(chan error, 1)
	go func() { _, err := al.processMessage(t.Context(), msg); finished <- err }()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not start")
	}
	reset := msg
	reset.Content = "/clear"
	if response, err := al.processMessage(t.Context(), reset); err != nil || !strings.Contains(response, "cleared") {
		t.Fatalf("reset = %q, %v", response, err)
	}
	close(provider.release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active turn did not finish")
	}
	nextProvider := &recordingProvider{}
	agent.Provider = nextProvider
	msg.Content = "new conversation"
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if input := conversationInput(nextProvider.lastMessages); strings.Contains(input, "OLD_IN_FLIGHT") || strings.Contains(input, "OLD_LATE_ANSWER") {
		t.Fatalf("late turn restored reset history: %s", input)
	}
}

type resetRaceProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *resetRaceProvider) GetDefaultModel() string { return "mock-model" }
func (p *resetRaceProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	close(p.started)
	<-p.release // Simulate an API that finishes after cancellation.
	return &providers.LLMResponse{Content: "OLD_LATE_ANSWER"}, nil
}

func conversationInput(messages []providers.Message) string {
	var input strings.Builder
	for _, message := range messages {
		input.WriteString(message.Content)
		input.WriteByte('\n')
	}
	return input.String()
}
