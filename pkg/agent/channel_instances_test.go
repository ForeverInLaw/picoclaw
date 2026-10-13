package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNamedTelegramConversationsStayIsolatedAfterConfigRoundTrip(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	cfg.ChannelList = config.ChannelList{
		"work": {Type: "telegram", Settings: json.RawMessage(`{"token":"work-secret"}`)},
	}
	cfg.Session.DMScope = "main"
	provider := &recordingProvider{}
	al.GetRegistry().GetDefaultAgent().Provider = provider
	legacy := selectedInlineMessage("42", "legacy-target", "LEGACY_INLINE_RETAINED")
	if _, err := al.processMessage(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	named := selectedInlineMessage("42", "named-target", "NAMED_INLINE_ONLY")
	named.Channel = "work"
	if _, err := al.processMessage(t.Context(), named); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conversationInput(provider.lastMessages), "LEGACY_INLINE_RETAINED") {
		t.Fatal("named inline imported legacy instance history")
	}
	direct := bus.InboundMessage{
		Channel:  "work",
		ChatID:   "42",
		SenderID: "telegram:42",
		Sender:   named.Sender,
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "PRIVATE_NAMED_DIRECT",
	}
	legacyDirect := direct
	legacyDirect.Channel = "telegram"
	legacyDirect.Content = "PRIVATE_LEGACY_DIRECT"
	if _, err := al.processMessage(t.Context(), legacyDirect); err != nil {
		t.Fatal(err)
	}
	direct.SessionKey = "agent:main:main"
	if _, err := al.processMessage(t.Context(), direct); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conversationInput(provider.lastMessages), "NAMED_INLINE_ONLY") ||
		strings.Contains(conversationInput(provider.lastMessages), "PRIVATE_LEGACY_DIRECT") {
		t.Fatal("named direct imported another conversation's history")
	}
	al.Close()
	path := cfg.WorkspacePath() + "/config.json"
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	provider = &recordingProvider{}
	reopened := NewAgentLoop(reloaded, bus.NewMessageBus(), provider)
	defer reopened.Close()
	named.Content = "Continue named inline"
	if _, err := reopened.processMessage(t.Context(), named); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(provider.lastMessages)
	if !strings.Contains(input, "NAMED_INLINE_ONLY") || strings.Contains(input, "PRIVATE_NAMED_DIRECT") ||
		strings.Contains(input, "LEGACY_INLINE_RETAINED") {
		t.Fatalf("named inline history not isolated after restart: %s", input)
	}
	legacy.Content = "Continue legacy inline"
	if _, err := reopened.processMessage(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if input = conversationInput(
		provider.lastMessages,
	); !strings.Contains(input, "LEGACY_INLINE_RETAINED") ||
		strings.Contains(input, "NAMED_INLINE_ONLY") {
		t.Fatalf("legacy inline identity changed: %s", input)
	}
}
