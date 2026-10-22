package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type inactivityScriptProvider struct {
	calls           []string
	primaryCanceled bool
}

func (*inactivityScriptProvider) GetDefaultModel() string { return "primary" }
func (p *inactivityScriptProvider) Chat(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, model string, _ map[string]any) (*providers.LLMResponse, error) {
	p.calls = append(p.calls, model)
	if model == "primary" {
		<-ctx.Done()
		p.primaryCanceled = errors.Is(ctx.Err(), context.Canceled)
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "backup completed"}, nil
}

func TestStalledModelYieldsToBackupInDirectAndInlineTurns(t *testing.T) {
	for _, chat := range []string{"42", "inline:chosen"} {
		t.Run(chat, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
				p := &inactivityScriptProvider{}
				a := al.registry.GetDefaultAgent()
				a.Provider = p
				a.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "primary"}, {Provider: "openai", Model: "backup"}}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				started := time.Now()
				answer, err := al.processMessage(ctx, bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"})
				if err != nil || !strings.HasSuffix(answer, "backup completed") || strings.Join(p.calls, ",") != "primary,backup" || !p.primaryCanceled || ctx.Err() != nil || time.Since(started) != time.Minute {
					t.Fatalf("answer=%q err=%v calls=%v childCanceled=%t parent=%v elapsed=%v", answer, err, p.calls, p.primaryCanceled, ctx.Err(), time.Since(started))
				}
			})
		})
	}
}
