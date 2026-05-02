package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type summaryRetryProvider struct {
	failures  int
	calls     int
	err       error
	content   string
	lastModel string
}

func (p *summaryRetryProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	p.lastModel = model
	if p.calls <= p.failures {
		if p.err != nil {
			return nil, p.err
		}
		return &providers.LLMResponse{Content: ""}, nil
	}
	return &providers.LLMResponse{Content: p.content}, nil
}

func (p *summaryRetryProvider) GetDefaultModel() string { return "summary-test-model" }

func withFastSummaryRetries(t *testing.T) {
	t.Helper()
	old := summaryLLMRetryInterval
	summaryLLMRetryInterval = 0
	t.Cleanup(func() { summaryLLMRetryInterval = old })
}

func TestSummarizeWithRetryRecoversAfterProviderErrors(t *testing.T) {
	withFastSummaryRetries(t)
	cfg := testConfig(t)
	provider := &summaryRetryProvider{
		failures: 2,
		err:      errors.New("temporary provider timeout"),
		content:  "recovered summary",
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(al.Close)
	agent := al.registry.GetDefaultAgent()

	got := al.summarizeWithRetry(context.Background(), agent, "summarize this", 256)
	if got != "recovered summary" {
		t.Fatalf("summary = %q, want recovered summary", got)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d, want 3", provider.calls)
	}
}

func TestSummarizeWithRetryUsesResolvedCandidateModel(t *testing.T) {
	withFastSummaryRetries(t)
	cfg := testConfig(t)
	cfg.Agents.Defaults.ModelName = "gemma-4-31b-it gouter"
	provider := &summaryRetryProvider{content: "resolved summary"}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(al.Close)
	agent := al.registry.GetDefaultAgent()
	agent.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "gemma-4-31b-it"}}

	got := al.summarizeWithRetry(context.Background(), agent, "summarize this", 256)
	if got != "resolved summary" {
		t.Fatalf("summary = %q, want resolved summary", got)
	}
	if provider.lastModel != "gemma-4-31b-it" {
		t.Fatalf("model = %q, want resolved candidate model", provider.lastModel)
	}
}
func TestSummarizeWithRetryUsesThirtyAttemptsBeforeGivingUp(t *testing.T) {
	withFastSummaryRetries(t)
	cfg := testConfig(t)
	provider := &summaryRetryProvider{failures: summaryLLMRetryLimit + 1}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(al.Close)
	agent := al.registry.GetDefaultAgent()

	got := al.summarizeWithRetry(context.Background(), agent, "summarize this", 256)
	if got != "" {
		t.Fatalf("summary = %q, want empty after exhausted retries", got)
	}
	if provider.calls != summaryLLMRetryLimit {
		t.Fatalf("provider calls = %d, want %d", provider.calls, summaryLLMRetryLimit)
	}
}

func TestCompressContext_StoresSummaryOnlyInSession(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.ContextWindow = 32000

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "compressed summary"})
	t.Cleanup(al.Close)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	sessionKey := "session-summary-only"
	history := []providers.Message{
		{Role: "user", Content: "head-1"},
		{Role: "assistant", Content: "head-2"},
		{Role: "assistant", Content: "head-3"},
		{Role: "assistant", Content: "head-4"},
		{Role: "user", Content: strings.Repeat("m", 12000)},
		{Role: "assistant", Content: strings.Repeat("n", 12000)},
		{Role: "user", Content: strings.Repeat("t", 12000)},
		{Role: "assistant", Content: strings.Repeat("z", 12000)},
	}
	agent.Sessions.SetHistory(sessionKey, history)

	result, ok := al.CompressContext(context.Background(), agent, sessionKey)
	if !ok {
		t.Fatal("expected compression to succeed")
	}
	if result.SummaryLen != len("compressed summary") {
		t.Fatalf("expected summary len %d, got %d", len("compressed summary"), result.SummaryLen)
	}

	summary := agent.Sessions.GetSummary(sessionKey)
	if summary != "compressed summary" {
		t.Fatalf("expected session summary to be stored, got %q", summary)
	}

	newHistory := agent.Sessions.GetHistory(sessionKey)
	if len(newHistory) >= len(history) {
		t.Fatalf("expected history to shrink, got %d messages from %d", len(newHistory), len(history))
	}
	for _, msg := range newHistory {
		if msg.Content == "compressed summary" {
			t.Fatal("summary must not be injected back into history")
		}
	}
}

func TestCompressContext_PreservesTailToolSequenceAtTurnBoundary(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.ContextWindow = 32000

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "merged summary"})
	t.Cleanup(al.Close)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	sessionKey := "session-tool-tail"
	history := []providers.Message{
		{Role: "user", Content: "head-1"},
		{Role: "assistant", Content: "head-2"},
		{Role: "assistant", Content: "head-3"},
		{Role: "assistant", Content: "head-4"},
		{Role: "user", Content: strings.Repeat("m", 6000)},
		{Role: "assistant", Content: strings.Repeat("n", 6000)},
		{Role: "user", Content: "tool question"},
		{
			Role:    "assistant",
			Content: "calling tool",
			ToolCalls: []providers.ToolCall{
				{ID: "call_1", Name: "lookup"},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: strings.Repeat("r", 12000)},
		{Role: "assistant", Content: "final answer"},
	}
	agent.Sessions.SetHistory(sessionKey, history)

	if _, ok := al.CompressContext(context.Background(), agent, sessionKey); !ok {
		t.Fatal("expected compression to succeed")
	}

	newHistory := agent.Sessions.GetHistory(sessionKey)
	wantTail := history[6:]
	if len(newHistory) < len(wantTail) {
		t.Fatalf("expected kept tail of %d messages, got %d", len(wantTail), len(newHistory))
	}
	gotTail := newHistory[len(newHistory)-len(wantTail):]
	if gotTail[0].Role != "user" {
		t.Fatalf("expected preserved tail to start at user boundary, got %q", gotTail[0].Role)
	}
	for i := range wantTail {
		if gotTail[i].Role != wantTail[i].Role {
			t.Fatalf("tail message %d role mismatch: got %q want %q", i, gotTail[i].Role, wantTail[i].Role)
		}
		if gotTail[i].ToolCallID != wantTail[i].ToolCallID {
			t.Fatalf("tail message %d toolCallID mismatch: got %q want %q", i, gotTail[i].ToolCallID, wantTail[i].ToolCallID)
		}
		if gotTail[i].Content != wantTail[i].Content {
			t.Fatalf("tail message %d content mismatch", i)
		}
	}
	if len(gotTail[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool-call message to remain intact, got %d tool calls", len(gotTail[1].ToolCalls))
	}
}
