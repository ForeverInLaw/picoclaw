package anthropicprovider

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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
