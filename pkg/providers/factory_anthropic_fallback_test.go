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

func TestAnthropicAPIKeyModelReturnsAnswerServedByFallbackModel(t *testing.T) {
	const (
		// A non-streaming decline comes back with the fallback block first and
		// the fallback model's answer after it.
		nonStreaming = `{"id":"msg_fb","type":"message","role":"assistant","model":"claude-opus-4-8",
			"stop_reason":"end_turn","stop_details":null,"content":[
				{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"},
				 "trigger":{"type":"refusal","category":"cyber"}},
				{"type":"text","text":"Here is the port scan explanation."}],
			"usage":{"input_tokens":12,"output_tokens":6,
				"iterations":[{"type":"message","model":"claude-opus-5-5","input_tokens":12,"output_tokens":0},
					{"type":"fallback_message","model":"claude-opus-4-8","input_tokens":12,"output_tokens":6}]}}`
		// A mid-output decline on a stream: message_start already named the
		// requested model, the fallback block marks the handoff, and the fallback
		// model continues the partial text. Thinking before the handoff is dropped.
		messageStart  = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fs\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n"
		thinking      = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"declined reasoning\",\"signature\":\"\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
		partialText   = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"Here is \"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"
		fallbackBlock = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"fallback\",\"from\":{\"model\":\"claude-opus-5-5\"},\"to\":{\"model\":\"claude-opus-4-8\"},\"trigger\":{\"type\":\"refusal\",\"category\":\"cyber\"}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n"
		restText      = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":3,\"content_block\":{\"type\":\"text\",\"text\":\"the port scan explanation.\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":3}\n\n"
		messageDelta  = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_details\":null},\"usage\":{\"output_tokens\":6}}\n\n"
		messageStop   = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	)
	for _, tc := range []struct {
		name    string
		respond func(w http.ResponseWriter)
		call    func(*testing.T, LLMProvider, string) (*LLMResponse, error)
	}{
		{name: "non-streaming", respond: func(w http.ResponseWriter) { writeJSON(w, nonStreaming) }, call: chatHi},
		{
			name: "streaming mid-output",
			respond: func(w http.ResponseWriter) {
				writeSSE(w, messageStart, thinking, partialText, fallbackBlock, restText, messageDelta, messageStop)
			},
			call: streamHi,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAnthropic(t, tc.respond)
			provider, modelID := anthropicModelProvider(t, fake, "claude-opus-5-5")

			var resp *LLMResponse
			var err error
			logged := usageLog(t, func() { resp, err = tc.call(t, provider, modelID) })
			if err != nil {
				t.Fatalf("call error = %v", err)
			}

			if resp.Content != "Here is the port scan explanation." || resp.FinishReason != "stop" || resp.Reasoning != "" {
				t.Errorf("response = %q/%q reasoning %q, want the fallback answer, stop, no reasoning",
					resp.Content, resp.FinishReason, resp.Reasoning)
			}
			if resp.ServedModel != "claude-opus-4-8" {
				t.Errorf("served model = %q, want claude-opus-4-8", resp.ServedModel)
			}
			if len(logged) != 1 {
				t.Fatalf("logged %d usage lines, want 1", len(logged))
			}
			if logged[0]["model"] != "claude-opus-4-8" || logged[0]["requested_model"] != "claude-opus-5-5" {
				t.Errorf("logged model = %v, requested_model = %v; want claude-opus-4-8 serving claude-opus-5-5",
					logged[0]["model"], logged[0]["requested_model"])
			}
		})
	}
}

const (
	// anthropicRefusal is a refusal that survived the server-side fallback.
	anthropicRefusal = `{"id":"msg_r","type":"message","role":"assistant","model":"claude-opus-5-5",
		"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber",
			"explanation":"This request could enable cyber harm.","recommended_model":null},
		"content":[],"usage":{"input_tokens":12,"output_tokens":0}}`
	anthropicRefusalText = "Claude declined to answer this request (cyber). This request could enable cyber harm."
)

func TestAnthropicAPIKeyModelSurfacesFinalRefusal(t *testing.T) {
	const (
		uncategorized = `{"id":"msg_r2","type":"message","role":"assistant","model":"claude-opus-5-5",
			"stop_reason":"refusal","stop_details":{"type":"refusal","category":null,"explanation":null},
			"content":[],"usage":{"input_tokens":12,"output_tokens":0}}`
		// A mid-stream refusal after partial output: the partial text is discarded.
		messageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_rs\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n"
		partialText  = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Sure, the exploit\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
		messageDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"category\":\"cyber\",\"explanation\":\"This request could enable cyber harm.\",\"recommended_model\":null}},\"usage\":{\"output_tokens\":4}}\n\n"
		messageStop  = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	)
	for _, tc := range []struct {
		name    string
		respond func(w http.ResponseWriter)
		call    func(*testing.T, LLMProvider, string) (*LLMResponse, error)
		want    string
	}{
		{name: "non-streaming", respond: func(w http.ResponseWriter) { writeJSON(w, anthropicRefusal) }, call: chatHi,
			want: anthropicRefusalText},
		{name: "uncategorized", respond: func(w http.ResponseWriter) { writeJSON(w, uncategorized) }, call: chatHi,
			want: "Claude declined to answer this request."},
		{
			name:    "streaming mid-output",
			respond: func(w http.ResponseWriter) { writeSSE(w, messageStart, partialText, messageDelta, messageStop) },
			call:    streamHi,
			want:    anthropicRefusalText,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAnthropic(t, tc.respond)
			provider, modelID := anthropicModelProvider(t, fake, "claude-opus-5-5")

			resp, err := tc.call(t, provider, modelID)
			if err != nil {
				t.Fatalf("call error = %v, want the refusal as an answer", err)
			}
			if resp.Content != tc.want || resp.FinishReason != "content_filter" || len(resp.ToolCalls) != 0 {
				t.Errorf("response = %q/%q with %d tool calls, want %q/content_filter",
					resp.Content, resp.FinishReason, len(resp.ToolCalls), tc.want)
			}
		})
	}
}

func TestFallbackChain_AnthropicFinalRefusalDoesNotFailOver(t *testing.T) {
	for name, provider := range anthropicProviders(t, http.StatusOK, anthropicRefusal) {
		t.Run(name, func(t *testing.T) {
			result, reserveCalls, _, err := runAnthropicThenReserve(t, provider)
			if err != nil {
				t.Fatalf("Execute() error = %v, want the refusal as the answer", err)
			}
			if reserveCalls != 0 {
				t.Fatalf("reserve calls = %d, want none", reserveCalls)
			}
			if result.Response.Content != anthropicRefusalText || result.Response.FinishReason != "content_filter" {
				t.Fatalf("response = %+v, want the refusal", result.Response)
			}
		})
	}
}
