package providers

import (
	"net/http"
	"slices"
	"testing"
)

const serverFallbackBeta = "server-side-fallback-2026-07-01"

// anthropicModelProvider creates the configured anthropic/<model> provider against fake.
func anthropicModelProvider(t *testing.T, fake *fakeAnthropic, model string) (LLMProvider, string) {
	t.Helper()
	cfg := anthropicAPIKeyModel(fake.URL)
	cfg.Model = "anthropic/" + model
	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	return provider, modelID
}

func chatHi(t *testing.T, provider LLMProvider, modelID string) (*LLMResponse, error) {
	t.Helper()
	return provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, modelID, nil)
}

func streamHi(t *testing.T, provider LLMProvider, modelID string) (*LLMResponse, error) {
	t.Helper()
	return provider.(StreamingLLMProvider).ChatStream(t.Context(),
		[]Message{{Role: "user", Content: "Hi"}}, nil, modelID, nil, func(string) {})
}

func TestAnthropicAPIKeyModelOptsSupportingModelsIntoServerFallback(t *testing.T) {
	const textStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_f\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	calls := map[string]func(*testing.T, LLMProvider, string) (*LLMResponse, error){
		"non-streaming": chatHi,
		"streaming":     streamHi,
	}
	for _, tc := range []struct {
		model    string
		fallback bool
	}{
		{model: "claude-opus-5-5", fallback: true},
		{model: "claude-opus-5.5", fallback: true},
		{model: "claude-fable-5-1", fallback: true},
		{model: "claude-fable-5", fallback: true},
		{model: "claude-opus-5", fallback: true},
		{model: "claude-sonnet-5-5", fallback: true},
		{model: "claude-haiku-5-5"},
		{model: "claude-sonnet-5"},
		{model: "claude-opus-4-8"},
		{model: "claude-sonnet-4.6"},
		{model: "claude-some-future-model"},
	} {
		for mode, call := range calls {
			t.Run(tc.model+"/"+mode, func(t *testing.T) {
				fake := newFakeAnthropic(t, func(w http.ResponseWriter) {
					if mode == "streaming" {
						writeSSE(w, textStream)
						return
					}
					writeJSON(w, cacheTestReply)
				})
				provider, modelID := anthropicModelProvider(t, fake, tc.model)

				if _, err := call(t, provider, modelID); err != nil {
					t.Fatalf("call error = %v", err)
				}

				req := fake.only(t)
				fallbacks, sent := req.body["fallbacks"]
				hasBeta := slices.Contains(req.header.Values("Anthropic-Beta"), serverFallbackBeta)
				if tc.fallback && (fallbacks != "default" || !hasBeta) {
					t.Errorf("fallbacks = %v, beta headers = %v; want \"default\" with %s",
						fallbacks, req.header.Values("Anthropic-Beta"), serverFallbackBeta)
				}
				if !tc.fallback && (sent || hasBeta) {
					t.Errorf("fallbacks = %v, beta headers = %v; want neither",
						fallbacks, req.header.Values("Anthropic-Beta"))
				}
			})
		}
	}
}
