package anthropicprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// The setup-token path maps thinking_level too, and its capability lookup
// carries the subscription token rather than an API key.
func TestProvider_SetupTokenMapsThinkingLevel(t *testing.T) {
	var lookupAuth, lookupBeta string
	var thinking, outputConfig json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models/claude-opus-5-5" {
			lookupAuth, lookupBeta = r.Header.Get("Authorization"), r.Header.Get("Anthropic-Beta")
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(
				w,
				`{"type":"model","id":"claude-opus-5-5","display_name":"Opus","created_at":"2026-01-01T00:00:00Z",
				"capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},
					"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}},
				"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"between_tools":{"supported":false},
					"disabled":{"supported":false},"enabled":{"supported":false}}}}}`,
			)
			return
		}
		var body struct {
			Thinking     json.RawMessage `json:"thinking"`
			OutputConfig json.RawMessage `json:"output_config"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		thinking, outputConfig = body.Thinking, body.OutputConfig
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(
			w,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\","+
				"\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		)
	}))
	defer server.Close()
	p := NewProviderWithTokenSourceAndBaseURL("stale", func() (string, error) { return "setup-token", nil }, server.URL)

	_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "claude-opus-5-5",
		map[string]any{"thinking_level": "off"})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if lookupAuth != "Bearer setup-token" || lookupBeta != anthropicBetaHeader {
		t.Errorf(
			"capability lookup auth = %q / beta %q, want the setup token with the OAuth beta",
			lookupAuth,
			lookupBeta,
		)
	}
	if string(thinking) != `{"type":"adaptive"}` || string(outputConfig) != `{"effort":"low"}` {
		t.Errorf("thinking = %s, output_config = %s; want adaptive with low effort", thinking, outputConfig)
	}
}

// unmarshalBlocks constructs []ContentBlockUnion via JSON round-trip so that
// the internal JSON.raw field is populated (required by AsText/AsThinking).
func unmarshalBlocks(t *testing.T, jsonStr string) []anthropic.ContentBlockUnion {
	t.Helper()
	var blocks []anthropic.ContentBlockUnion
	if err := json.Unmarshal([]byte(jsonStr), &blocks); err != nil {
		t.Fatalf("unmarshalBlocks: %v", err)
	}
	return blocks
}

func TestParseResponse_ThinkingBlock(t *testing.T) {
	resp := &anthropic.Message{
		Content: unmarshalBlocks(t, `[
			{"type":"thinking","thinking":"Let me reason step by step...","signature":"sig"},
			{"type":"text","text":"The answer is 42."}
		]`),
		StopReason: anthropic.StopReasonEndTurn,
	}

	result := parseResponse(resp)

	if result.Reasoning != "Let me reason step by step..." {
		t.Errorf("Reasoning = %q, want thinking content", result.Reasoning)
	}
	if result.Content != "The answer is 42." {
		t.Errorf("Content = %q, want text content", result.Content)
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", result.FinishReason)
	}
}

func TestParseResponse_NoThinkingBlock(t *testing.T) {
	resp := &anthropic.Message{
		Content: unmarshalBlocks(t, `[
			{"type":"text","text":"Just a normal response."}
		]`),
		StopReason: anthropic.StopReasonEndTurn,
	}

	result := parseResponse(resp)

	if result.Reasoning != "" {
		t.Errorf("Reasoning = %q, want empty", result.Reasoning)
	}
	if result.Content != "Just a normal response." {
		t.Errorf("Content = %q, want text content", result.Content)
	}
}

// capabilityServer answers the Models API for claude-sonnet-4-6, which accepts
// disabled thinking, with status for every key but goodKey. It counts lookups.
func capabilityServer(t *testing.T, goodKey string, status *int) (*httptest.Server, *int) {
	t.Helper()
	lookups := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/models/") {
			lookups++
			if r.Header.Get("X-Api-Key") != goodKey {
				w.WriteHeader(*status)
				io.WriteString(w, `{"type":"error","error":{"type":"error","message":"no"}}`)
				return
			}
			io.WriteString(
				w,
				`{"type":"model","id":"claude-sonnet-4-6","display_name":"Sonnet","created_at":"2026-01-01T00:00:00Z",
				"capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},
					"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":true}},
				"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"between_tools":{"supported":false},
					"disabled":{"supported":true},"enabled":{"supported":true}}}}}`,
			)
			return
		}
		io.WriteString(
			w,
			`{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-6","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)
	}))
	t.Cleanup(server.Close)
	return server, &lookups
}

// useCapabilityCache gives the test its own capability cache on a clock it moves.
func useCapabilityCache(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	previous := modelCapabilities
	modelCapabilities = newCapabilityCache(func() time.Time { return now })
	t.Cleanup(func() { modelCapabilities = previous })
	return &now
}

func thinkingOf(t *testing.T, p *Provider) anthropic.ThinkingConfigParamUnion {
	t.Helper()
	params, err := p.requestParams(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "claude-sonnet-4-6",
		map[string]any{"thinking_level": "off"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return params.Thinking
}

func TestCapabilityLookupFailureIsRetriedAfterAWhile(t *testing.T) {
	now := useCapabilityCache(t)
	status := http.StatusInternalServerError
	server, lookups := capabilityServer(t, "", &status)
	p := NewProviderWithAPIKey("sk-ant-key", server.URL, "", 0)

	thinkingOf(t, p)
	thinkingOf(t, p)
	if *lookups != 1 {
		t.Fatalf("lookups within the retry interval = %d, want 1", *lookups)
	}
	*now = now.Add(capabilityRetryAfter)
	thinkingOf(t, p)
	if *lookups != 2 {
		t.Fatalf("lookups after the retry interval = %d, want 2", *lookups)
	}
}

func TestCapabilityLookupRejectedByOneKeyDoesNotDecideForOthers(t *testing.T) {
	useCapabilityCache(t)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, _ := capabilityServer(t, "sk-ant-good", &status)
			if thinking := thinkingOf(
				t,
				NewProviderWithAPIKey("sk-ant-bad", server.URL, "", 0),
			); thinking.OfAdaptive == nil {
				t.Fatalf("rejected lookup: thinking = %+v, want adaptive with the lowest effort", thinking)
			}
			if thinking := thinkingOf(
				t,
				NewProviderWithAPIKey("sk-ant-good", server.URL, "", 0),
			); thinking.OfDisabled == nil {
				t.Fatalf("other key after a rejected lookup: thinking = %+v, want disabled", thinking)
			}
		})
	}
}
