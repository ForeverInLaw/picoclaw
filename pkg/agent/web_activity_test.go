package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/channels/pico"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type webActivityProvider struct{ scriptedToolProvider }

func (p *webActivityProvider) ChatStreamContent(
	ctx context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	opts map[string]any,
	update func(providers.StreamContent),
) (*providers.LLMResponse, error) {
	update(providers.StreamContent{Phase: providers.ContentPhaseReasoning, Content: "Provider-supplied thought"})
	response, err := p.Chat(ctx, messages, defs, model, opts)
	response.ReasoningContent = "Provider-supplied thought"
	if len(response.ToolCalls) == 0 {
		update(providers.StreamContent{Phase: providers.ContentPhaseAnswer, Content: "do"})
	}
	return response, err
}

type webNonStreamingProvider struct{ scriptedToolProvider }

func (p *webNonStreamingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	response, err := p.scriptedToolProvider.Chat(ctx, messages, defs, model, opts)
	response.Reasoning = "Non-stream supplied thought"
	return response, err
}

func TestWebChatNonStreamingThoughtAndToolErrorKeepAnswerSeparate(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	defer mb.Close()
	provider := &webNonStreamingProvider{}
	al.GetRegistry().GetDefaultAgent().Provider = provider
	al.RegisterTool(
		&activityContractTool{result: tools.ErrorResult("PRIVATE_LLM_TOOL_DETAIL").WithActivity("Public tool failure")},
	)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	defer func() { cancel(); <-done }()
	publishIncoming(
		t,
		mb,
		bus.InboundMessage{
			Channel:   "pico",
			SenderID:  "pico:pico-user",
			ChatID:    "pico:non-stream",
			MessageID: "request-error",
			Content:   "run tool",
			Peer:      bus.Peer{Kind: "direct", ID: "pico:non-stream"},
		},
	)
	var sawThought, sawError bool
	for {
		select {
		case event := <-mb.OutboundChan():
			if strings.Contains(event.Content, "PRIVATE_") ||
				event.Progress != nil && strings.Contains(event.Progress.Result+event.Progress.Error, "PRIVATE_") {
				t.Fatal("LLM-only tool output reached web activity")
			}
			if event.Progress == nil {
				if event.Content != "done" || !sawThought || !sawError {
					t.Fatalf("final=%q thought=%v error=%v", event.Content, sawThought, sawError)
				}
				return
			}
			if event.Progress.Kind == bus.ProgressReasoning {
				sawThought = event.Progress.Result == "Non-stream supplied thought"
			}
			if event.Progress.Kind == bus.ProgressTool && event.Progress.State == bus.ProgressFailed {
				sawError = event.Progress.Result == "Public tool failure" &&
					event.Progress.Error == "Public tool failure"
			}
		case <-time.After(3 * time.Second):
			t.Fatal("web turn did not complete")
		}
	}
}

func TestWebChatStreamsThoughtToolActivityAndOneFinal(t *testing.T) {
	for _, name := range []string{"pico", "dashboard-main"} {
		t.Run(name, func(t *testing.T) { runWebChatActivityScenario(t, name) })
	}
}

func runWebChatActivityScenario(t *testing.T, channelName string) {
	al, cfg, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	defer mb.Close()
	cfg.Channels.Pico = config.PicoConfig{Enabled: true, Token: "web-activity-token"}
	if channelName != "pico" {
		cfg.Channels.Pico.Enabled = false
		cfg.ChannelList = config.ChannelList{
			channelName: &config.Channel{
				Enabled:  true,
				Type:     "pico",
				Settings: json.RawMessage(`{"token":"web-activity-token"}`),
			},
		}
	}
	manager, err := channels.NewManager(cfg, mb, nil)
	if err != nil {
		t.Fatal(err)
	}
	al.SetChannelManager(manager)
	if err = manager.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer manager.StopAll(t.Context())
	channel, ok := manager.GetChannel(channelName)
	if !ok {
		t.Fatal("Pico not registered")
	}
	server := httptest.NewServer(channel.(*pico.PicoChannel))
	defer server.Close()
	provider := &webActivityProvider{}
	al.GetRegistry().GetDefaultAgent().Provider = provider
	al.RegisterTool(&mockCustomTool{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	defer func() { cancel(); <-done }()
	conn, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/pico/ws?session_id=activity",
		http.Header{"Authorization": []string{"Bearer web-activity-token"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	defer conn.Close()
	if err = conn.WriteJSON(
		pico.PicoMessage{
			Type:    pico.TypeMessageSend,
			ID:      "web-request-1",
			Payload: map[string]any{"content": "run tool"},
		},
	); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var thought string
	toolStates := make(map[string]bool)
	var finalID string
	for {
		var event pico.PicoMessage
		if err = conn.ReadJSON(&event); err != nil {
			t.Fatalf("missing thought/tool/final: thought=%q states=%v final=%q: %v", thought, toolStates, finalID, err)
		}
		if event.Type == "activity.update" {
			if event.Payload["kind"] == "reasoning" {
				thought, _ = event.Payload["result"].(string)
			}
			if event.Payload["kind"] == "tool" {
				state, _ := event.Payload["state"].(string)
				toolStates[state] = true
			}
		}
		if event.Type == pico.TypeMessageCreate && event.Payload["content"] == "done" {
			finalID, _ = event.Payload["message_id"].(string)
		}
		if thought != "" && toolStates["started"] && toolStates["completed"] && finalID != "" {
			break
		}
	}
	if thought != "Provider-supplied thought" {
		t.Fatalf("thought=%q", thought)
	}
	conn.Close()
	reconnected, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/pico/ws?session_id=activity",
		http.Header{"Authorization": []string{"Bearer web-activity-token"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	defer reconnected.Close()
	reconnected.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var event pico.PicoMessage
		if err = reconnected.ReadJSON(&event); err != nil {
			t.Fatalf("lost final after reconnect: %v", err)
		}
		if event.Type == pico.TypeMessageCreate && event.Payload["content"] == "done" {
			if event.Payload["message_id"] != finalID {
				t.Fatalf("reconnect changed final identity: %v", event.Payload)
			}
			break
		}
	}
}
