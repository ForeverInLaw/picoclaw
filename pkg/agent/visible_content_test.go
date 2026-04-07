package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSanitizeVisibleAssistantContent_StripsThoughtBlocks(t *testing.T) {
	input := "<thought>The user is asking something</thought>\nНа картинке кот."
	got := sanitizeVisibleAssistantContent(input)
	if got != "На картинке кот." {
		t.Fatalf("sanitizeVisibleAssistantContent() = %q, want %q", got, "На картинке кот.")
	}
}

func TestSanitizeVisibleAssistantContent_StripsDanglingThoughtTail(t *testing.T) {
	input := "Ответ пользователю\n<thought>hidden reasoning continues"
	got := sanitizeVisibleAssistantContent(input)
	if got != "Ответ пользователю" {
		t.Fatalf("sanitizeVisibleAssistantContent() = %q, want %q", got, "Ответ пользователю")
	}
}

func TestSanitizeVisibleAssistantContent_StripsStrayThinkingTags(t *testing.T) {
	input := "<thinking>secret</thinking>Visible<thought x=\"1\">internal</thought> answer"
	got := sanitizeVisibleAssistantContent(input)
	if got != "Visible answer" {
		t.Fatalf("sanitizeVisibleAssistantContent() = %q, want %q", got, "Visible answer")
	}
}

func TestOfferStreamingContent_StripsThoughtBlocks(t *testing.T) {
	streamer := &fakeStreamingSink{}
	offerStreamingContent(context.Background(), streamer, nil, "<thought>secret</thought>Visible answer")
	if len(streamer.updates) != 1 || streamer.updates[0] != "Visible answer" {
		t.Fatalf("streamer updates = %#v, want Visible answer", streamer.updates)
	}
}

type reasoningOnlyProvider struct{}

func (p *reasoningOnlyProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{ReasoningContent: "<thought>hidden</thought>final?"}, nil
}

func (p *reasoningOnlyProvider) GetDefaultModel() string { return "reasoning-only" }

func TestProcessMessage_DoesNotExposeReasoningContentFallback(t *testing.T) {
	_, cfg, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	al := NewAgentLoop(cfg, msgBus, &reasoningOnlyProvider{})
	response, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "6669548787",
		Content: "тест",
	})
	if err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}
	if response != defaultResponse {
		t.Fatalf("processMessage() response = %q, want %q", response, defaultResponse)
	}
	if response == "final?" {
		t.Fatal("reasoning content leaked to visible response")
	}
}
