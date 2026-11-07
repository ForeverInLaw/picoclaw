package anthropicprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
