package agent

import (
	"context"
	"github.com/sipeed/picoclaw/pkg/providers"
	"strings"
	"testing"
)

type answerScriptProvider struct {
	responses []*providers.LLMResponse
	snapshots [][]string
	calls     int
}

func (p *answerScriptProvider) GetDefaultModel() string { return "answer-script" }
func (p *answerScriptProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	r := p.responses[min(p.calls, len(p.responses)-1)]
	p.calls++
	return r, nil
}
func (p *answerScriptProvider) ChatStream(ctx context.Context, m []providers.Message, t []providers.ToolDefinition, model string, opts map[string]any, update func(string)) (*providers.LLMResponse, error) {
	if p.calls < len(p.snapshots) {
		for _, s := range p.snapshots[p.calls] {
			update(s)
		}
	}
	return p.Chat(ctx, m, t, model, opts)
}

func TestEmptyAnswerRecoversWithoutExposingReasoning(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	p := &answerScriptProvider{responses: []*providers.LLMResponse{{ReasoningContent: "SECRET REASONING"}, {Content: "recovered answer"}}}
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

func TestAmbiguousToolCommentaryNeverEntersAnswerPreview(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	sink := &fakeStreamingSink{}
	mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
	p := &answerScriptProvider{responses: []*providers.LLMResponse{{Content: "I will inspect a file", ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "mock_custom", Arguments: map[string]any{"task": "ping"}}}}, {Content: "The answer"}}, snapshots: [][]string{{"I will", "I will inspect a file"}, {"The", "The answer"}}}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	al.RegisterTool(&mockCustomTool{})
	got, err := al.runAgentLoop(context.Background(), a, processOptions{SessionKey: "stream-answer", Channel: "telegram", ChatID: "42", UserMessage: "inspect"})
	if err != nil || got != "The answer" {
		t.Fatalf("answer=%q err=%v", got, err)
	}
	if len(sink.finalized) != 1 || sink.finalized[0] != "The answer" {
		t.Fatalf("finalized=%v", sink.finalized)
	}
	for _, update := range sink.updates {
		if strings.Contains(update, "I will") {
			t.Fatalf("tool commentary in preview: %v", sink.updates)
		}
	}
}

func TestEmptyAnswerRecoveryIsBounded(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	p := &answerScriptProvider{responses: []*providers.LLMResponse{{ReasoningContent: "private"}}}
	al.registry.GetDefaultAgent().Provider = p
	got, err := al.ProcessDirect(context.Background(), "hello", "empty-exhaustion")
	if got != "" || err == nil || !strings.Contains(err.Error(), "no answer") || p.calls != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", got, err, p.calls)
	}
}

type phasedAnswerScriptProvider struct{ answerScriptProvider }

func (p *phasedAnswerScriptProvider) ChatStreamContent(ctx context.Context, m []providers.Message, t []providers.ToolDefinition, model string, opts map[string]any, update func(providers.StreamContent)) (*providers.LLMResponse, error) {
	update(providers.StreamContent{Content: "PRIVATE", Phase: providers.ContentPhaseReasoning})
	update(providers.StreamContent{Content: "Looking things up", Phase: providers.ContentPhaseCommentary})
	update(providers.StreamContent{Content: "The", Phase: providers.ContentPhaseAnswer})
	return p.Chat(ctx, m, t, model, opts)
}

func TestPhasedStreamingShowsOnlyAnswerText(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	sink := &fakeStreamingSink{}
	mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
	p := &phasedAnswerScriptProvider{answerScriptProvider: answerScriptProvider{responses: []*providers.LLMResponse{{Content: "The answer", ContentPhase: providers.ContentPhaseAnswer}}}}
	a := al.registry.GetDefaultAgent()
	a.Provider = p
	got, err := al.runAgentLoop(context.Background(), a, processOptions{SessionKey: "phased-answer", Channel: "telegram", ChatID: "42", UserMessage: "hello"})
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
