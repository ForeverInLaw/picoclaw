package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/personaltodo"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
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

type personalTodoThenOverthinkProvider struct {
	call int
}

func (p *personalTodoThenOverthinkProvider) Chat(
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
					ID:   "call_todo_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "personal_todo",
						Arguments: `{"action":"list","scope":"open"}`,
					},
				},
			},
		}, nil
	}
	return &providers.LLMResponse{
		ToolCalls: []providers.ToolCall{
			{
				ID:   "call_chat_memory_1",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      "chat_memory",
					Arguments: `{"mode":"search","query":"todo list"}`,
				},
			},
		},
	}, nil
}

func (p *personalTodoThenOverthinkProvider) GetDefaultModel() string {
	return "mock-model"
}

func newMessageDeliveryLoop(t *testing.T) (*AgentLoop, *config.Config, *bus.MessageBus) {
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
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		Tools: config.ToolsConfig{
			Message: config.ToolConfig{Enabled: true},
		},
	}

	msgBus := bus.NewMessageBus()
	return NewAgentLoop(cfg, msgBus, &messageThenDoneProvider{}), cfg, msgBus
}

func newPersonalTodoTerminalLoop(
	t *testing.T,
) (*AgentLoop, *config.Config, *bus.MessageBus, *personalTodoThenOverthinkProvider) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "agent-todo-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = tmpDir
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 10
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	cfg.Tools.Message.Enabled = true

	msgBus := bus.NewMessageBus()
	provider := &personalTodoThenOverthinkProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	store, err := personaltodo.Open(filepath.Join(tmpDir, "state", "personal_todos.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Add(context.Background(), "telegram:42", "buy milk", "telegram", "42"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	tool, ok := al.registry.GetDefaultAgent().Tools.Get("personal_todo")
	if !ok {
		t.Fatal("personal_todo tool not registered")
	}
	todoTool, ok := tool.(*tools.PersonalTodoTool)
	if !ok {
		t.Fatalf("personal_todo tool has unexpected type %T", tool)
	}
	todoTool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		return publishToolMessage(msgBus, ctx, channel, chatID, content)
	})

	return al, cfg, msgBus, provider
}

func TestProcessMessage_PersistsDeliveredReplyInsteadOfMetaAck(t *testing.T) {
	al, _, _ := newMessageDeliveryLoop(t)

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
	al, cfg, _ := newMessageDeliveryLoop(t)
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

func TestProcessMessage_DoesNotPublishDuplicateFinalResponseAfterMessageTool(t *testing.T) {
	al, _, msgBus := newMessageDeliveryLoop(t)

	_, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "telegram:42",
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		ChatID:   "42",
		Content:  "reply using the message tool",
	})
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}

	var outbound []bus.OutboundMessage
	for {
		select {
		case msg := <-msgBus.OutboundChan():
			outbound = append(outbound, msg)
		default:
			if len(outbound) != 1 {
				t.Fatalf("expected exactly 1 outbound message, got %d: %+v", len(outbound), outbound)
			}
			if outbound[0].Content != "VISIBLE_REPLY" {
				t.Fatalf("unexpected outbound content: %+v", outbound[0])
			}
			return
		}
	}
}

func TestProcessMessage_StopsAfterTerminalPersonalTodoTool(t *testing.T) {
	al, _, msgBus, provider := newPersonalTodoTerminalLoop(t)

	response, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "telegram:42",
		Sender: bus.SenderInfo{
			Platform:    "telegram",
			PlatformID:  "42",
			CanonicalID: "telegram:42",
			DisplayName: "Tester",
		},
		Peer:    bus.Peer{Kind: "direct", ID: "42"},
		ChatID:  "42",
		Content: "какой мой список дел",
	})
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	if response != "" {
		t.Fatalf("expected empty final response after terminal todo tool, got %q", response)
	}
	if provider.call != 1 {
		t.Fatalf("expected provider to stop after first call, got %d", provider.call)
	}

	var outbound []bus.OutboundMessage
	for {
		select {
		case msg := <-msgBus.OutboundChan():
			outbound = append(outbound, msg)
		default:
			if len(outbound) != 1 {
				t.Fatalf("expected exactly 1 outbound message, got %d: %+v", len(outbound), outbound)
			}
			if !strings.Contains(outbound[0].Content, "#1 buy milk") {
				t.Fatalf("unexpected outbound content: %+v", outbound[0])
			}
			return
		}
	}
}
