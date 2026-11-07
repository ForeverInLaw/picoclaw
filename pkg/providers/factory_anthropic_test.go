package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// fakeAnthropic plays the Messages API and records what each request carried.
type fakeAnthropic struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedAnthropicRequest
}

type recordedAnthropicRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

// newFakeAnthropic answers every /v1/messages request with the given response
// writer after recording it.
func newFakeAnthropic(t *testing.T, respond func(w http.ResponseWriter)) *fakeAnthropic {
	t.Helper()
	fake := &fakeAnthropic{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, recordedAnthropicRequest{
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   body,
		})
		fake.mu.Unlock()
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		respond(w)
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeAnthropic) only(t *testing.T) recordedAnthropicRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatalf("fake Anthropic received %d requests, want 1", len(f.requests))
	}
	return f.requests[0]
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func writeSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = io.WriteString(w, event)
	}
}

func anthropicAPIKeyModel(apiBase string) *config.ModelConfig {
	return &config.ModelConfig{
		ModelName: "claude",
		Model:     "anthropic/claude-sonnet-4.6",
		APIKey:    "sk-ant-test",
		APIBase:   apiBase,
	}
}

func TestAnthropicAPIKeyModelUsesMessagesAPIWithAPIKeyHeader(t *testing.T) {
	fake := newFakeAnthropic(t, func(w http.ResponseWriter) {
		writeJSON(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",
			"stop_reason":"end_turn","content":[{"type":"text","text":"Hello there"}],
			"usage":{"input_tokens":11,"output_tokens":3}}`)
	})

	provider, modelID, err := CreateProviderFromConfig(anthropicAPIKeyModel(fake.URL + "/v1"))
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	resp, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, modelID,
		map[string]any{"max_tokens": 512, "temperature": 0.7})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	req := fake.only(t)
	if req.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", req.path)
	}
	if got := req.header.Get("X-Api-Key"); got != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want sk-ant-test", got)
	}
	if got := req.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
	if req.body["model"] != "claude-sonnet-4-6" {
		t.Errorf("model = %v, want claude-sonnet-4-6", req.body["model"])
	}
	if _, ok := req.body["temperature"]; ok {
		t.Errorf("request carried temperature %v, want none", req.body["temperature"])
	}
	if resp.Content != "Hello there" || resp.FinishReason != "stop" {
		t.Errorf("response = %q/%q, want Hello there/stop", resp.Content, resp.FinishReason)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v, want 11 prompt / 3 completion", resp.Usage)
	}
}

// toolConversation is a turn that already used one tool and returned its result.
func toolConversation() ([]Message, []ToolDefinition) {
	messages := []Message{
		{Role: "system", SystemParts: []ContentBlock{
			{Type: "text", Text: "static rules", CacheControl: &CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "dynamic context"},
		}},
		{Role: "user", Content: "Weather in Paris?"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "toolu_1", Name: "get_weather", Arguments: map[string]any{"city": "Paris"}},
		}},
		{Role: "tool", ToolCallID: "toolu_1", Content: `{"temp":18}`},
	}
	tools := []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{
		Name:        "get_weather",
		Description: "Weather for a city",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []any{"city"},
		},
	}}}
	return messages, tools
}

func TestAnthropicAPIKeyModelRoundTripsToolsAndSystemParts(t *testing.T) {
	fake := newFakeAnthropic(t, func(w http.ResponseWriter) {
		writeJSON(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-6",
			"stop_reason":"tool_use","content":[
				{"type":"text","text":"Checking London too."},
				{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{"city":"London"}}],
			"usage":{"input_tokens":40,"output_tokens":12}}`)
	})
	provider, modelID, err := CreateProviderFromConfig(anthropicAPIKeyModel(fake.URL))
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	messages, tools := toolConversation()

	resp, err := provider.Chat(t.Context(), messages, tools, modelID, map[string]any{"max_tokens": 512})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	body := fake.only(t).body
	assertJSON(t, "system", body["system"], `[
		{"type":"text","text":"static rules","cache_control":{"type":"ephemeral"}},
		{"type":"text","text":"dynamic context"}]`)
	assertJSON(t, "messages", body["messages"], `[
		{"role":"user","content":[{"type":"text","text":"Weather in Paris?"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"{\"temp\":18}"}],"is_error":false}]}]`)
	assertJSON(t, "tools", body["tools"], `[{"name":"get_weather","description":"Weather for a city",
		"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`)

	if resp.FinishReason != "tool_calls" || resp.Content != "Checking London too." {
		t.Errorf("response = %q/%q, want Checking London too./tool_calls", resp.Content, resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_2" || resp.ToolCalls[0].Name != "get_weather" ||
		resp.ToolCalls[0].Arguments["city"] != "London" {
		t.Errorf("tool calls = %+v, want get_weather(London) as toolu_2", resp.ToolCalls)
	}
}

// assertJSON compares a decoded request field with the expected JSON document.
func assertJSON(t *testing.T, field string, got any, want string) {
	t.Helper()
	var expected any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("bad expected JSON for %s: %v", field, err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(expected)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s =\n%s\nwant\n%s", field, gotJSON, wantJSON)
	}
}

func TestAnthropicAPIKeyModelStreamsTextAndToolCalls(t *testing.T) {
	fake := newFakeAnthropic(t, func(w http.ResponseWriter) {
		writeSSE(
			w,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-6\",\"stop_reason\":null,\"usage\":{\"input_tokens\":20,\"output_tokens\":0}}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" check.\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_3\",\"name\":\"get_weather\",\"input\":{}}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Oslo\\\"}\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		)
	})
	provider, modelID, err := CreateProviderFromConfig(anthropicAPIKeyModel(fake.URL))
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	streamer, ok := provider.(StreamingLLMProvider)
	if !ok {
		t.Fatalf("provider %T does not stream", provider)
	}
	messages, tools := toolConversation()

	var snapshots []string
	resp, err := streamer.ChatStream(t.Context(), messages, tools, modelID,
		map[string]any{"max_tokens": 512, "temperature": 0.2},
		func(content string) { snapshots = append(snapshots, content) })
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	req := fake.only(t)
	if req.header.Get("X-Api-Key") != "sk-ant-test" || req.header.Get("Authorization") != "" {
		t.Errorf("auth headers = x-api-key %q / Authorization %q, want API key only",
			req.header.Get("X-Api-Key"), req.header.Get("Authorization"))
	}
	if req.body["stream"] != true {
		t.Errorf("stream = %v, want true", req.body["stream"])
	}
	if _, ok := req.body["temperature"]; ok {
		t.Errorf("request carried temperature %v, want none", req.body["temperature"])
	}
	if !slices.Equal(snapshots, []string{"Let me", "Let me check."}) {
		t.Errorf("snapshots = %#v, want progressive text", snapshots)
	}
	if resp.Content != "Let me check." || resp.FinishReason != "tool_calls" {
		t.Errorf("response = %q/%q, want Let me check./tool_calls", resp.Content, resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_3" || resp.ToolCalls[0].Arguments["city"] != "Oslo" {
		t.Errorf("tool calls = %+v, want get_weather(Oslo) as toolu_3", resp.ToolCalls)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 20 || resp.Usage.CompletionTokens != 9 {
		t.Errorf("usage = %+v, want 20 prompt / 9 completion", resp.Usage)
	}
}
