package anthropicprovider

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// serverFallbackModels are the models documented to accept Anthropic's
// server-side refusal fallback. Haiku 5.5 has classifiers but no fallback, and
// older or unknown models are not documented to accept the field.
var serverFallbackModels = map[anthropic.Model]bool{
	anthropic.ModelClaudeFable5_1:  true,
	anthropic.ModelClaudeFable5:    true,
	anthropic.ModelClaudeOpus5_5:   true,
	anthropic.ModelClaudeOpus5:     true,
	anthropic.ModelClaudeSonnet5_5: true,
}

// serverFallbackOptions opts a request to a supporting model into
// `fallbacks: "default"`, so a request its safety classifier declines is
// re-served by Anthropic's recommended fallback model within the same call.
// Only API-key providers opt in; setup-token requests stay as they were.
func (p *Provider) serverFallbackOptions(model anthropic.Model) []option.RequestOption {
	if !p.serverFallback || !serverFallbackModels[model] {
		return nil
	}
	return []option.RequestOption{
		option.WithHeaderAdd("anthropic-beta", string(anthropic.AnthropicBetaServerSideFallback2026_07_01)),
		option.WithJSONSet("fallbacks", "default"),
	}
}

// servedModel is the model that produced msg: the target of its last fallback
// block, or else the model the response names. A stream declined mid-output
// still names the requested model, from message_start.
func servedModel(msg *anthropic.Message) anthropic.Model {
	for i := len(msg.Content) - 1; i >= 0; i-- {
		if msg.Content[i].Type != "fallback" {
			continue
		}
		var block anthropic.BetaFallbackBlock
		if err := block.UnmarshalJSON([]byte(msg.Content[i].RawJSON())); err == nil && block.To.Model != "" {
			return block.To.Model
		}
		break
	}
	return msg.Model
}

// refusalResponse surfaces a refusal that survived any server-side fallback as
// an answer, so neither the agent nor the Routing chain treats it as a provider
// failure. Output streamed before a mid-stream refusal is incomplete and dropped.
func refusalResponse(msg *anthropic.Message) *LLMResponse {
	details := msg.StopDetails
	logger.WarnCF("provider.anthropic", "Anthropic refusal", map[string]any{
		"model":    string(servedModel(msg)),
		"category": string(details.Category),
	})
	text := "Claude declined to answer this request"
	if details.Category != "" {
		text += " (" + string(details.Category) + ")"
	}
	text += "."
	if details.Explanation != "" {
		text += " " + details.Explanation
	}
	return &LLMResponse{Content: text, FinishReason: "content_filter", Usage: usageInfo(msg.Usage)}
}
