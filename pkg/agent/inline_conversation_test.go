package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSelectedInlineConversationContinuesAcrossDestinationsAndRestart(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.GetRegistry().GetDefaultAgent()
	if err := agent.ContextBuilder.memory.WriteLongTerm("# Saved facts\n\nPRIVATE_SAVED_FACT"); err != nil {
		t.Fatal(err)
	}
	if err := agent.ContextBuilder.memory.AppendToday("PRIVATE_DAILY_NOTE"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(agent.Workspace, "USER.md"),
		[]byte("PRIVATE_USER_PROFILE"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	direct := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "PRIVATE_DIRECT_DIALOG",
	}
	if _, err := al.processMessage(t.Context(), direct); err != nil {
		t.Fatal(err)
	}
	msg := selectedInlineMessage("42", "first-destination", "INLINE_FIRST_SELECTED")
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	al.Close()
	provider := &recordingProvider{}
	reopened := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer reopened.Close()
	msg = selectedInlineMessage("42", "other-destination", "Continue the selected conversation")
	if _, err := reopened.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(provider.lastMessages)
	if !strings.Contains(input, "INLINE_FIRST_SELECTED") || !strings.Contains(input, "Mock response") {
		t.Fatalf("selected inline history lost across destination/restart: %s", input)
	}
	for _, private := range []string{"PRIVATE_SAVED_FACT", "PRIVATE_DAILY_NOTE", "PRIVATE_DIRECT_DIALOG", "PRIVATE_USER_PROFILE"} {
		if strings.Contains(input, private) {
			t.Errorf("inline input leaked %s", private)
		}
	}
	msg = selectedInlineMessage("43", "same-destination", "Another user's query")
	if _, err := reopened.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conversationInput(provider.lastMessages), "INLINE_FIRST_SELECTED") {
		t.Fatal("another Telegram user received inline history")
	}
}

func selectedInlineMessage(uid, destination, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel: "telegram", ChatID: "inline:" + destination, SenderID: "telegram:" + uid,
		Sender: bus.SenderInfo{Platform: "telegram", PlatformID: uid, CanonicalID: "telegram:" + uid},
		Peer:   bus.Peer{Kind: "inline", ID: uid}, Content: content,
		Metadata: map[string]string{telegramInlineMetadataKey: "true", telegramInlineQueryKey: content},
	}
}

func TestClearInlineResetsOnlyInvokingUsersInlineConversationFromDirectChat(t *testing.T) {
	al, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer messageBus.Close()
	defer al.Close()
	direct := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Sender:   bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42"},
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "DIRECT_RETAINED",
	}
	for _, msg := range []bus.InboundMessage{direct, selectedInlineMessage("42", "first", "INLINE_RESET_ME"), selectedInlineMessage("43", "other", "OTHER_INLINE_RETAINED")} {
		if _, err := al.processMessage(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
	}
	direct.Content = "/clear_inline"
	if response, err := al.processMessage(
		t.Context(),
		direct,
	); err != nil ||
		!strings.Contains(response, "Inline conversation reset") {
		t.Fatalf("inline reset = %q, %v", response, err)
	}
	provider := &recordingProvider{}
	al.GetRegistry().GetDefaultAgent().Provider = provider
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "new-destination", "Start fresh"),
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conversationInput(provider.lastMessages), "INLINE_RESET_ME") {
		t.Fatal("inline reset retained previous dialog")
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("43", "another", "Continue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "OTHER_INLINE_RETAINED") {
		t.Fatal("inline reset cleared another user")
	}
	direct.Content = "Continue direct"
	if _, err := al.processMessage(t.Context(), direct); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "DIRECT_RETAINED") {
		t.Fatal("inline reset cleared the direct dialog")
	}
	group := direct
	group.ChatID = "-1001"
	group.Peer = bus.Peer{Kind: "group", ID: "-1001"}
	group.Content = "/clear_inline"
	if response, err := al.processMessage(
		t.Context(),
		group,
	); err != nil ||
		!strings.Contains(response, "unavailable") {
		t.Fatalf("group inline reset = %q, %v", response, err)
	}
	direct.Content = "/clear"
	if response, err := al.processMessage(t.Context(), direct); err != nil || !strings.Contains(response, "cleared") {
		t.Fatalf("direct reset = %q, %v", response, err)
	}
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("43", "after-direct-reset", "Continue again"),
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "OTHER_INLINE_RETAINED") {
		t.Fatal("normal conversation reset cleared inline history")
	}
}

func TestClearInlineReportsPersistenceFailure(t *testing.T) {
	al, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer messageBus.Close()
	defer al.Close()
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "first", "INLINE_RETAIN_ON_FAILURE"),
	); err != nil {
		t.Fatal(err)
	}
	agent := al.GetRegistry().GetDefaultAgent()
	if err := os.WriteFile(
		filepath.Join(agent.Workspace, "sessions", "boundaries"),
		[]byte("unwritable directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "/clear_inline",
	}
	if response, err := al.processMessage(
		t.Context(),
		msg,
	); err != nil ||
		!strings.Contains(response, "Failed to reset inline") {
		t.Fatalf("failed inline reset = %q, %v", response, err)
	}
	if err := os.Remove(filepath.Join(agent.Workspace, "sessions", "boundaries")); err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{}
	agent.Provider = provider
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "second", "Continue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "INLINE_RETAIN_ON_FAILURE") {
		t.Fatal("failed inline reset discarded selected history")
	}
}

func TestInlineHistoryIsCompressedBeforeItExceedsModelContext(t *testing.T) {
	al, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer messageBus.Close()
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	agent.ContextWindow = 100000
	agent.SummarizeMessageThreshold = 100000
	for i := 0; i < 12; i++ {
		if _, err := al.processMessage(
			t.Context(),
			selectedInlineMessage("42", "destination", strings.Repeat("x", 1000)),
		); err != nil {
			t.Fatal(err)
		}
	}
	agent.ContextWindow = 4000
	agent.MaxTokens = 256
	provider := &recordingProvider{}
	agent.Provider = provider
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "another-destination", "Continue compactly"),
	); err != nil {
		t.Fatal(err)
	}
	// The model has fewer than 4k input tokens available. Even the project's
	// generous 2.5-character estimate cannot fit a 10k-character prompt.
	if size := utf8.RuneCountInString(conversationInput(provider.lastMessages)); size > 10000 {
		t.Fatalf("oversized inline context reached the model: %d characters", size)
	}
	if !strings.Contains(provider.lastMessages[0].Content, "CONTEXT_SUMMARY") {
		t.Fatal("compression did not preserve an inline summary")
	}
}

func TestInlineConversationIsolatesAgentsAndAllAutomaticPrivateContext(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	al.Close()
	workspace := cfg.Agents.Defaults.Workspace
	cfg.Agents.Defaults.MemoryIndex = config.MemoryIndexConfig{
		Enabled:         true,
		MaxResults:      5,
		MaxSnippetChars: 500,
		MinQueryChars:   3,
	}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Workspace: workspace},
		{ID: "other", Workspace: workspace},
	}
	cfg.Bindings = []config.AgentBinding{
		{AgentID: "other", Match: config.BindingMatch{Channel: "telegram", AccountID: "other-bot"}},
	}
	provider := &recordingProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	for _, msg := range []bus.InboundMessage{
		{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Peer: bus.Peer{Kind: "direct", ID: "42"}, Content: "PRIVATE_DIRECT_BICYCLE bicycle"},
		{Channel: "telegram", ChatID: "-1001", SenderID: "telegram:42", Peer: bus.Peer{Kind: "group", ID: "-1001"}, Content: "PRIVATE_GROUP_BICYCLE bicycle"},
		selectedInlineMessage("42", "first", "MAIN_INLINE_BICYCLE bicycle"),
	} {
		if _, err := al.processMessage(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
	}
	for _, private := range []string{"PRIVATE_DIRECT_BICYCLE", "PRIVATE_GROUP_BICYCLE"} {
		if strings.Contains(conversationInput(provider.lastMessages), private) {
			t.Errorf("inline context leaked %s", private)
		}
	}
	msg := selectedInlineMessage("42", "other-bot-destination", "OTHER_INLINE_BICYCLE bicycle")
	msg.Metadata[metadataKeyAccountID] = "other-bot"
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conversationInput(provider.lastMessages), "MAIN_INLINE_BICYCLE") {
		t.Fatal("same user received another agent's inline history")
	}
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "main-again", "Recall inline bicycle"),
	); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(provider.lastMessages)
	if !strings.Contains(input, "MAIN_INLINE_BICYCLE") || strings.Contains(input, "OTHER_INLINE_BICYCLE") {
		t.Fatalf("agent isolation/continuity failed: %s", input)
	}
}
