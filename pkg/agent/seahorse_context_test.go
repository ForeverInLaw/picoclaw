package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSeahorseCompactsActiveContextWithoutDeletingCanonicalTranscript(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	al.Close()
	// Settings go through the real config/constructor boundary.
	var settings config.Config
	if err := settings.UnmarshalJSON([]byte(`{"agents":{"defaults":{"context_manager":"seahorse"}}}`)); err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults = settings.Agents.Defaults
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Agents.Defaults.ContextWindow = 7000
	provider := &recordingProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	for i := 0; i < 24; i++ {
		if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "target", strings.Repeat("source detail ", 70))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "target", "Continue after compaction")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "summary") {
		t.Fatal("source-linked context was not compressed")
	}
	history := al.GetRegistry().GetDefaultAgent().Sessions.GetHistory(inlineConversationKey("main", "42"))
	if len(history) != 50 {
		t.Fatalf("canonical transcript was truncated: %d", len(history))
	}
}
