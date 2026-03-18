package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type messageThenDoneProvider struct {
	call int
}

func (p *messageThenDoneProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.call++
	if p.call == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_message_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "message",
						Arguments: `{"content":"VISIBLE_REPLY"}`,
					},
				},
			},
		}, nil
	}
	return &providers.LLMResponse{
		Content:   "Done.",
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (p *messageThenDoneProvider) GetDefaultModel() string {
	return "mock-model"
}

func newMessageDeliveryLoop(t *testing.T) (*AgentLoop, *config.Config) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		Tools: config.ToolsConfig{
			Message: config.ToolConfig{Enabled: true},
		},
	}

	return NewAgentLoop(cfg, bus.NewMessageBus(), &messageThenDoneProvider{}), cfg
}

func TestProcessMessage_PersistsDeliveredReplyInsteadOfMetaAck(t *testing.T) {
	al, _ := newMessageDeliveryLoop(t)

	_, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "telegram:42",
		Sender: bus.SenderInfo{
			DisplayName: "Tester",
		},
		ChatID:  "42",
		Content: "reply using the message tool",
	})
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}

	history := defaultAgent.Sessions.GetHistory("agent:main:main")
	if len(history) == 0 {
		t.Fatal("expected session history")
	}

	last := history[len(history)-1]
	if last.Role != "assistant" || last.Content != "VISIBLE_REPLY" {
		t.Fatalf("last history message = %+v, want assistant VISIBLE_REPLY", last)
	}
}

func TestProcessMessage_PersistsDeliveredReply_ForExplicitAgentScopedSessionKey(t *testing.T) {
	al, cfg := newMessageDeliveryLoop(t)
	sessionKey := "agent:main:telegram:direct:42:memfix-check"

	_, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel:    "telegram",
		SenderID:   "telegram:42",
		ChatID:     "42",
		Content:    "reply using the message tool",
		SessionKey: sessionKey,
	})
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}

	history := defaultAgent.Sessions.GetHistory(sessionKey)
	if len(history) == 0 {
		t.Fatal("expected session history")
	}

	last := history[len(history)-1]
	if last.Role != "assistant" || last.Content != "VISIBLE_REPLY" {
		t.Fatalf("last history message = %+v, want assistant VISIBLE_REPLY", last)
	}

	sessionPath := filepath.Join(cfg.Agents.Defaults.Workspace, "sessions", "agent_main_telegram_direct_42_memfix-check.jsonl")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", sessionPath, err)
	}
	if !strings.Contains(string(data), "VISIBLE_REPLY") {
		t.Fatalf("session file missing delivered reply:\n%s", string(data))
	}
	if strings.Contains(string(data), "\"content\":\"Done.\"") {
		t.Fatalf("session file still persisted meta ack:\n%s", string(data))
	}
}
