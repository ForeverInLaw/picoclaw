package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type answerScriptProvider struct {
	responses []*providers.LLMResponse
	snapshots [][]string
	calls     int
}

func (p *answerScriptProvider) GetDefaultModel() string { return "answer-script" }

func (p *answerScriptProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	r := p.responses[min(p.calls, len(p.responses)-1)]
	p.calls++
	return r, nil
}

func (p *answerScriptProvider) ChatStream(
	ctx context.Context,
	m []providers.Message,
	t []providers.ToolDefinition,
	model string,
	opts map[string]any,
	update func(string),
) (*providers.LLMResponse, error) {
	if p.calls < len(p.snapshots) {
		for _, s := range p.snapshots[p.calls] {
			update(s)
		}
	}
	return p.Chat(ctx, m, t, model, opts)
}

func TestEmptyAnswerRecoversWithoutExposingReasoning(t *testing.T) {
	al, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer messageBus.Close()
	p := &answerScriptProvider{
		responses: []*providers.LLMResponse{{ReasoningContent: "SECRET REASONING"}, {Content: "recovered answer"}},
	}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	got, err := al.ProcessDirect(context.Background(), "hello", "empty-recovery")
	if err != nil || got != "recovered answer" || p.calls != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", got, err, p.calls)
	}
	for _, msg := range a.Sessions.GetHistory("agent:main:main") {
		if strings.Contains(msg.Content, "SECRET REASONING") {
			t.Fatal("reasoning persisted as answer")
		}
	}
}

type answerPreviewProbeProvider struct {
	answerScriptProvider
	beforeReturn func(*providers.LLMResponse)
	streamErrors []error
}

func (p *answerPreviewProbeProvider) ChatStream(
	ctx context.Context,
	m []providers.Message,
	t []providers.ToolDefinition,
	model string,
	opts map[string]any,
	update func(string),
) (*providers.LLMResponse, error) {
	call := p.calls
	response, err := p.answerScriptProvider.ChatStream(ctx, m, t, model, opts, update)
	if call < len(p.streamErrors) && p.streamErrors[call] != nil {
		return nil, p.streamErrors[call]
	}
	if err == nil && p.beforeReturn != nil {
		p.beforeReturn(response)
	}
	return response, err
}

type attemptStreamDelegate struct{ streams []*fakeStreamingSink }

func (d *attemptStreamDelegate) GetStreamer(context.Context, string, string) (bus.Streamer, bool) {
	sink := &fakeStreamingSink{}
	d.streams = append(d.streams, sink)
	return sink, true
}

func TestStreamingFallbackCancelsFailedPreviewAndStartsFreshStream(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	delegate := &attemptStreamDelegate{}
	mb.SetStreamDelegate(delegate)
	p := &answerPreviewProbeProvider{
		answerScriptProvider: answerScriptProvider{
			responses: []*providers.LLMResponse{{Content: "Failed preview"}, {Content: "Recovered answer"}},
			snapshots: [][]string{{"Failed preview"}, {"Recovered", "Recovered answer"}},
		},
		streamErrors: []error{fmt.Errorf("status: 503")},
	}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	a.Candidates = []providers.FallbackCandidate{
		{Provider: "openai", Model: "one"}, {Provider: "openai", Model: "two"},
	}
	got, err := al.runAgentLoop(t.Context(), a, processOptions{
		SessionKey: "fallback-stream", Channel: "telegram", ChatID: "42", UserMessage: "hello",
	})
	if err != nil || got != "Recovered answer" {
		t.Fatalf("answer=%q err=%v", got, err)
	}
	if len(delegate.streams) != 2 {
		t.Fatalf("fallback must start a fresh stream: count=%d", len(delegate.streams))
	}
	failed, recovered := delegate.streams[0], delegate.streams[1]
	if failed.canceled != 1 || len(failed.finalized) != 0 || len(failed.updates) != 1 {
		t.Fatalf("failed preview lifecycle: %+v", failed)
	}
	if recovered.canceled != 0 || len(recovered.finalized) != 1 || recovered.updates[0] != "Recovered" {
		t.Fatalf("recovered stream lifecycle: %+v", recovered)
	}
}

func TestCompatibilityAnswerStreamsBeforeProviderReturnsAfterTools(t *testing.T) {
	for _, destination := range []struct{ channel, chatID string }{
		{"telegram", "42"}, {"telegram", "-42"}, {"telegram", "-42/7"},
		{"telegram", "inline:guest-answer"}, {"discord", "42"},
	} {
		t.Run(destination.channel+"/"+destination.chatID, func(t *testing.T) {
			al, _, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			sink := &fakeStreamingSink{}
			mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
			p := &answerPreviewProbeProvider{
				answerScriptProvider: answerScriptProvider{
					responses: []*providers.LLMResponse{
						{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "mock_custom", Arguments: map[string]any{"task": "ping"}}}},
						{Content: "The answer"},
					},
					snapshots: [][]string{nil, {"The", "The answer"}},
				},
				beforeReturn: func(response *providers.LLMResponse) {
					if len(response.ToolCalls) == 0 && (len(sink.updates) < 2 || sink.updates[0] != "The") {
						t.Errorf("answer buffered until provider returned: previews=%v", sink.updates)
					}
				},
			}
			a := al.registry.GetDefaultAgent()
			a.Provider = p
			a.Candidates = []providers.FallbackCandidate{
				{Provider: "openai", Model: "test-model"},
				{Provider: "openai", Model: "backup-model"},
			}
			al.RegisterTool(&mockCustomTool{})
			got, err := al.runAgentLoop(t.Context(), a, processOptions{
				SessionKey: "compat-stream", Channel: destination.channel,
				ChatID: destination.chatID, UserMessage: "inspect",
			})
			if err != nil || got != "The answer" {
				t.Fatalf("answer=%q err=%v", got, err)
			}
		})
	}
}

func TestCompatibilityToolCallingPreviewIsCanceledBeforeFinalAnswer(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	sink := &fakeStreamingSink{}
	mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
	p := &answerScriptProvider{
		responses: []*providers.LLMResponse{
			{
				Content: "I will inspect a file",
				ToolCalls: []providers.ToolCall{
					{ID: "call-1", Name: "mock_custom", Arguments: map[string]any{"task": "ping"}},
				},
			},
			{Content: "The answer"},
		},
		snapshots: [][]string{{"I will", "I will inspect a file"}, {"The", "The answer"}},
	}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	al.RegisterTool(&mockCustomTool{})
	got, err := al.runAgentLoop(
		context.Background(),
		a,
		processOptions{SessionKey: "stream-answer", Channel: "telegram", ChatID: "42", UserMessage: "inspect"},
	)
	if err != nil || got != "The answer" {
		t.Fatalf("answer=%q err=%v", got, err)
	}
	if len(sink.finalized) != 1 || sink.finalized[0] != "The answer" {
		t.Fatalf("finalized=%v", sink.finalized)
	}
	if sink.canceled != 1 {
		t.Fatalf("tool-calling preview was not canceled: %d", sink.canceled)
	}
}

func TestEmptyAnswerRecoveryIsBounded(t *testing.T) {
	al, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer messageBus.Close()
	p := &answerScriptProvider{responses: []*providers.LLMResponse{{ReasoningContent: "private"}}}
	al.registry.GetDefaultAgent().Provider = p
	got, err := al.ProcessDirect(context.Background(), "hello", "empty-exhaustion")
	if got != "" || err == nil || !strings.Contains(err.Error(), "no answer") || p.calls != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", got, err, p.calls)
	}
	if !errors.Is(err, errEmptyAnswer) {
		t.Fatalf("no-answer failure lost its trusted source: %v", err)
	}
}

type phasedAnswerScriptProvider struct{ answerScriptProvider }

func (p *phasedAnswerScriptProvider) ChatStreamContent(
	ctx context.Context,
	m []providers.Message,
	t []providers.ToolDefinition,
	model string,
	opts map[string]any,
	update func(providers.StreamContent),
) (*providers.LLMResponse, error) {
	update(providers.StreamContent{Content: "PRIVATE", Phase: providers.ContentPhaseReasoning})
	update(providers.StreamContent{Content: "Looking things up", Phase: providers.ContentPhaseCommentary})
	update(providers.StreamContent{Content: "Unclassified content", Phase: providers.ContentPhaseUnknown})
	update(providers.StreamContent{Content: "The", Phase: providers.ContentPhaseAnswer})
	return p.Chat(ctx, m, t, model, opts)
}

func TestPhasedStreamingShowsOnlyAnswerText(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	sink := &fakeStreamingSink{}
	mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
	p := &phasedAnswerScriptProvider{
		answerScriptProvider: answerScriptProvider{
			responses: []*providers.LLMResponse{{Content: "The answer", ContentPhase: providers.ContentPhaseAnswer}},
		},
	}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	got, err := al.runAgentLoop(
		context.Background(),
		a,
		processOptions{SessionKey: "phased-answer", Channel: "telegram", ChatID: "42", UserMessage: "hello"},
	)
	if err != nil || got != "The answer" {
		t.Fatalf("answer=%q err=%v", got, err)
	}
	if len(sink.updates) != 2 || sink.updates[0] != "The" || sink.updates[1] != "The answer" {
		t.Fatalf("answer preview=%v", sink.updates)
	}
	if len(sink.finalized) != 1 {
		t.Fatalf("finalized=%v", sink.finalized)
	}
}
