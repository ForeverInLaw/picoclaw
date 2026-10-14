package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type inlineQuoteProvider struct {
	body     string
	requests [][]providers.Message
}

func (*inlineQuoteProvider) GetDefaultModel() string { return "inline-quote" }

func (p *inlineQuoteProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.requests = append(p.requests, append([]providers.Message(nil), messages...))
	return &providers.LLMResponse{Content: p.body, ContentPhase: providers.ContentPhaseAnswer}, nil
}

func (p *inlineQuoteProvider) ChatStream(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
	update func(string),
) (*providers.LLMResponse, error) {
	update(strings.Split(p.body, "\n\n")[0])
	update(p.body)
	return p.Chat(ctx, messages, tools, model, opts)
}

func TestInlineDeliveryQuoteStaysOutOfModelHistory(t *testing.T) {
	for _, engine := range []string{"legacy", "seahorse"} {
		for _, scenario := range []struct {
			name      string
			guest     bool
			streaming bool
		}{
			{name: "guest final", guest: true},
			{name: "guest stream", guest: true, streaming: true},
			{name: "selected inline final"},
			{name: "selected inline stream", streaming: true},
		} {
			t.Run(engine+"/"+scenario.name, func(t *testing.T) {
				cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
					Workspace: t.TempDir(), Model: "inline-quote", ContextManager: engine,
					MaxTokens: 256, MaxToolIterations: 1, ContextWindow: 32768,
					SummarizeMessageThreshold: 1000, SummarizeTokenPercent: 1000,
				}}}
				const body = "> Содержательная цитата модели\n\nОтвет без транспортной обёртки."
				provider := &inlineQuoteProvider{body: body}
				mb := bus.NewMessageBus()
				defer mb.Close()
				loop := NewAgentLoop(cfg, mb, provider)
				defer loop.Close()
				sink := &fakeStreamingSink{}
				if scenario.streaming {
					mb.SetStreamDelegate(&fakeStreamDelegate{streamer: sink})
				}
				msg := selectedInlineMessage("42", "first-publication", "Исходный запрос")
				if scenario.guest {
					msg.Metadata["telegram_guest"] = "true"
				}
				got, err := loop.processMessage(t.Context(), msg)
				require.NoError(t, err)
				want := body
				if !scenario.guest {
					want = "> Исходный запрос\n\n" + body
				}
				require.Equal(t, want, got)
				require.Len(t, provider.requests, 1, "one model request per publication")
				if scenario.streaming {
					require.Equal(t, []string{want}, sink.finalized)
					require.NotEmpty(t, sink.updates)
					for _, preview := range sink.updates {
						if scenario.guest {
							require.NotContains(t, preview, "Исходный запрос")
						} else {
							require.True(t, strings.HasPrefix(preview, "> Исходный запрос\n\n"))
						}
					}
				}
				loop.Close()
				nextProvider := &inlineQuoteProvider{body: "Следующий ответ"}
				nextBus := bus.NewMessageBus()
				defer nextBus.Close()
				reopened := NewAgentLoop(cfg, nextBus, nextProvider)
				defer reopened.Close()
				_, err = reopened.processMessage(
					t.Context(),
					selectedInlineMessage("42", "next-publication", "Продолжай"),
				)
				require.NoError(t, err)
				require.Len(t, nextProvider.requests, 1)
				var previousAnswers []string
				for _, message := range nextProvider.requests[0] {
					if message.Role == "assistant" {
						previousAnswers = append(previousAnswers, message.Content)
					}
				}
				require.Equal(
					t,
					[]string{body},
					previousAnswers,
					"history retains model quotes but excludes delivery quotes after restart",
				)
			})
		}
	}
}
