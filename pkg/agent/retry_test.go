package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type retryScriptProvider struct {
	calls     int
	failures  []error
	responses map[int]*providers.LLMResponse
	messages  [][]providers.Message
}

func (p *retryScriptProvider) GetDefaultModel() string { return "retry-script" }
func (p *retryScriptProvider) Chat(_ context.Context, m []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.messages = append(p.messages, append([]providers.Message(nil), m...))
	index := p.calls
	p.calls++
	if index < len(p.failures) && p.failures[index] != nil {
		return nil, p.failures[index]
	}
	if response, ok := p.responses[index]; ok {
		return response, nil
	}
	return &providers.LLMResponse{Content: "recovered"}, nil
}

func TestTransientModelFailuresRecoverInDirectAndInlineTurns(t *testing.T) {
	for _, chat := range []string{"42", "inline:chosen"} {
		t.Run(chat, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
				p := &retryScriptProvider{failures: []error{&net.OpError{Op: "read", Net: "tcp", Err: fmt.Errorf("connection reset by peer")}, fmt.Errorf("HTTP status: 503 temporarily unavailable")}}
				al.registry.GetDefaultAgent().Provider = p
				got, err := al.processMessage(context.Background(), bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"})
				if err != nil || !strings.HasSuffix(got, "recovered") || p.calls != 3 {
					t.Fatalf("answer=%q err=%v attempts=%d", got, err, p.calls)
				}
				status := <-mb.OutboundChan()
				if status.Progress == nil || status.Progress.Kind != bus.ProgressRetry || strings.TrimSpace(status.Content) == "" {
					t.Fatalf("retry status must be separate from answer: %+v", status)
				}
			})
		})
	}
}

func TestModelRetryPolicyBoundsAttemptsAndServerWaitInBothModes(t *testing.T) {
	cases := []struct {
		name     string
		failures []error
		attempts int
		elapsed  time.Duration
		success  bool
	}{
		{"timeout", []error{context.DeadlineExceeded}, 2, time.Second, true},
		{"exhausted", []error{fmt.Errorf("status: 503"), fmt.Errorf("status: 503"), fmt.Errorf("status: 503")}, 3, 3 * time.Second, false},
		{"authentication", []error{fmt.Errorf("status: 401 invalid api key")}, 1, 0, false},
		{"invalid_request", []error{fmt.Errorf("status: 400 invalid request")}, 1, 0, false},
		{"quota", []error{&protocoltypes.HTTPError{StatusCode: 429, Wrapped: fmt.Errorf("insufficient_quota")}}, 1, 0, false},
		{"server_delay", []error{&protocoltypes.HTTPError{StatusCode: 429, RetryAfter: 7 * time.Second, Wrapped: fmt.Errorf("rate_limit_exceeded")}}, 2, 7 * time.Second, true},
		{"server_delay_exceeds_budget", []error{&protocoltypes.HTTPError{StatusCode: 429, RetryAfter: 31 * time.Second, Wrapped: fmt.Errorf("rate_limit_exceeded")}}, 1, 0, false},
	}
	for _, chat := range []string{"42", "inline:chosen"} {
		for _, scenario := range cases {
			t.Run(chat+"/"+scenario.name, func(t *testing.T) {
				withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
					p := &retryScriptProvider{failures: scenario.failures}
					al.registry.GetDefaultAgent().Provider = p
					start := time.Now()
					got, err := al.processMessage(context.Background(), bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"})
					if (err == nil) != scenario.success || p.calls != scenario.attempts || time.Since(start) != scenario.elapsed {
						t.Fatalf("answer=%q error=%v attempts=%d elapsed=%v", got, err, p.calls, time.Since(start))
					}
				})
			})
		}
	}
}

func TestCancellationStopsModelRetryInBothModes(t *testing.T) {
	for _, chat := range []string{"42", "inline:chosen"} {
		t.Run(chat, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
				p := &retryScriptProvider{failures: []error{fmt.Errorf("status: 503")}}
				al.registry.GetDefaultAgent().Provider = p
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() { time.Sleep(500 * time.Millisecond); cancel() }()
				got, err := al.processMessage(ctx, bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"})
				if got != "" || !errors.Is(err, context.Canceled) || p.calls != 1 {
					t.Fatalf("answer=%q error=%v attempts=%d", got, err, p.calls)
				}
			})
		})
	}
}

type recordingActionTool struct {
	mockCustomTool
	executions int
}

func (t *recordingActionTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	t.executions++
	return tools.UserResult("action completed once")
}

func TestModelRetryRetainsToolResultWithoutReplayingAction(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
		tool := &recordingActionTool{}
		al.RegisterTool(tool)
		p := &retryScriptProvider{failures: []error{nil, fmt.Errorf("status: 503")}, responses: map[int]*providers.LLMResponse{0: {ToolCalls: []providers.ToolCall{{ID: "recorded", Name: "mock_custom", Arguments: map[string]any{"task": "action"}}}}}}
		al.registry.GetDefaultAgent().Provider = p
		got, err := al.processMessage(context.Background(), bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Content: "run action"})
		if err != nil || got != "recovered" || tool.executions != 1 || p.calls != 3 {
			t.Fatalf("answer=%q error=%v executions=%d attempts=%d", got, err, tool.executions, p.calls)
		}
		retained := false
		for _, msg := range p.messages[2] {
			if msg.Role == "tool" && msg.Content == "action completed once" {
				retained = true
			}
		}
		if !retained {
			t.Fatal("successful retry lost completed tool output")
		}
	})
}

// Keep persistent-store workers outside the virtual-time bubble; only turns use fake time.
func withRetryLoop(t *testing.T, run func(*testing.T, *AgentLoop, *bus.MessageBus)) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	synctest.Test(t, func(t *testing.T) { run(t, al, mb) })
}

func TestModelFallbackAndRecoveryShareAttemptBudget(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
		p := &retryScriptProvider{failures: []error{fmt.Errorf("status: 503"), fmt.Errorf("status: 503"), fmt.Errorf("status: 503"), fmt.Errorf("status: 503")}}
		a := al.registry.GetDefaultAgent()
		a.Provider = p
		a.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "one"}, {Provider: "openai", Model: "two"}, {Provider: "openai", Model: "three"}, {Provider: "openai", Model: "four"}}
		_, err := al.ProcessDirect(context.Background(), "hello", "fallback-budget")
		if err == nil || p.calls != 3 {
			t.Fatalf("error=%v actual provider attempts=%d", err, p.calls)
		}
	})
}

func TestEmptyAnswerRecoverySharesTransientAttemptBudget(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
		p := &retryScriptProvider{failures: []error{fmt.Errorf("status: 503"), nil, fmt.Errorf("status: 503")}, responses: map[int]*providers.LLMResponse{1: {}}}
		al.registry.GetDefaultAgent().Provider = p
		got, err := al.ProcessDirect(context.Background(), "hello", "mixed-budget")
		if err == nil || got != "" || p.calls != 3 {
			t.Fatalf("answer=%q error=%v attempts=%d", got, err, p.calls)
		}
	})
}
