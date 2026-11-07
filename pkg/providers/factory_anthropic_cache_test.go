package providers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const cacheTestReply = `{"id":"msg_c","type":"message","role":"assistant","model":"claude-sonnet-4-6",
	"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// cacheControls collects every cache_control object in a decoded request body.
func cacheControls(node any) []any {
	var found []any
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "cache_control" {
				found = append(found, child)
				continue
			}
			found = append(found, cacheControls(child)...)
		}
	case []any:
		for _, child := range v {
			found = append(found, cacheControls(child)...)
		}
	}
	return found
}

func TestAnthropicAPIKeyModelCachesStaticPrefixAndConversationTail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cacheTTL string
		want     string
	}{
		{name: "default is 5 minutes", want: `{"type":"ephemeral"}`},
		{name: "1 hour option", cacheTTL: "1h", want: `{"type":"ephemeral","ttl":"1h"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAnthropic(t, func(w http.ResponseWriter) { writeJSON(w, cacheTestReply) })
			cfg := anthropicAPIKeyModel(fake.URL)
			cfg.CacheTTL = tc.cacheTTL
			provider, modelID, err := CreateProviderFromConfig(cfg)
			if err != nil {
				t.Fatalf("CreateProviderFromConfig() error = %v", err)
			}
			messages, tools := toolConversation()

			if _, err := provider.Chat(t.Context(), messages, tools, modelID, nil); err != nil {
				t.Fatalf("Chat() error = %v", err)
			}

			body := fake.only(t).body
			if got := len(cacheControls(body)); got != 2 {
				t.Errorf("request carries %d cache points, want 2", got)
			}
			assertJSON(t, "cache_control", body["cache_control"], tc.want)
			assertJSON(t, "system", body["system"], `[
				{"type":"text","text":"static rules","cache_control":`+tc.want+`},
				{"type":"text","text":"dynamic context"}]`)
		})
	}
}

func TestAnthropicAPIKeyModelCachesToolsWhenNoStaticSystemPart(t *testing.T) {
	fake := newFakeAnthropic(t, func(w http.ResponseWriter) { writeJSON(w, cacheTestReply) })
	provider, modelID, err := CreateProviderFromConfig(anthropicAPIKeyModel(fake.URL))
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	_, tools := toolConversation()
	tools = append(tools, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{
		Name: "get_time", Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}})

	if _, err := provider.Chat(t.Context(), []Message{
		{Role: "system", Content: "summarize the conversation"},
		{Role: "user", Content: "Hi"},
	}, tools, modelID, nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	body := fake.only(t).body
	if got := len(cacheControls(body)); got != 2 {
		t.Errorf("request carries %d cache points, want 2", got)
	}
	toolList, _ := body["tools"].([]any)
	if len(toolList) != 2 {
		t.Fatalf("tools = %v, want 2", body["tools"])
	}
	assertJSON(t, "last tool cache_control", toolList[1].(map[string]any)["cache_control"], `{"type":"ephemeral"}`)
	assertJSON(t, "cache_control", body["cache_control"], `{"type":"ephemeral"}`)
}

// usageLog captures the per-request usage lines the provider logs while fn runs.
func usageLog(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := logger.EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	fn()
	logger.DisableFileLogging()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["message"] == "Anthropic usage" {
			lines = append(lines, entry)
		}
	}
	return lines
}

func TestAnthropicAPIKeyModelReportsCacheUsage(t *testing.T) {
	const (
		nonStreaming = `{"id":"msg_u","type":"message","role":"assistant","model":"claude-sonnet-4-6",
			"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":30,"output_tokens":7,"cache_creation_input_tokens":200,"cache_read_input_tokens":1800}}`
		// message_delta usage is cumulative and supersedes the counts of message_start.
		messageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-6\",\"stop_reason\":null,\"usage\":{\"input_tokens\":25,\"output_tokens\":1,\"cache_creation_input_tokens\":100,\"cache_read_input_tokens\":900}}}\n\n"
		textBlock    = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
		messageDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":30,\"output_tokens\":7,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":1800}}\n\n"
		messageStop  = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	)
	for _, tc := range []struct {
		name    string
		respond func(w http.ResponseWriter)
		call    func(provider LLMProvider, modelID string) (*LLMResponse, error)
	}{
		{
			name:    "non-streaming",
			respond: func(w http.ResponseWriter) { writeJSON(w, nonStreaming) },
			call: func(provider LLMProvider, modelID string) (*LLMResponse, error) {
				return provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, modelID, nil)
			},
		},
		{
			name:    "streaming",
			respond: func(w http.ResponseWriter) { writeSSE(w, messageStart, textBlock, messageDelta, messageStop) },
			call: func(provider LLMProvider, modelID string) (*LLMResponse, error) {
				return provider.(StreamingLLMProvider).ChatStream(t.Context(),
					[]Message{{Role: "user", Content: "Hi"}}, nil, modelID, nil, func(string) {})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAnthropic(t, tc.respond)
			provider, modelID, err := CreateProviderFromConfig(anthropicAPIKeyModel(fake.URL))
			if err != nil {
				t.Fatalf("CreateProviderFromConfig() error = %v", err)
			}

			var resp *LLMResponse
			logged := usageLog(t, func() { resp, err = tc.call(provider, modelID) })
			if err != nil {
				t.Fatalf("call error = %v", err)
			}

			want := UsageInfo{
				PromptTokens: 2030, CompletionTokens: 7, TotalTokens: 2037,
				CacheCreationTokens: 200, CacheReadTokens: 1800,
			}
			if resp.Usage == nil || *resp.Usage != want {
				t.Errorf("usage = %+v, want %+v", resp.Usage, want)
			}
			if len(logged) != 1 {
				t.Fatalf("logged %d usage lines, want 1", len(logged))
			}
			for field, value := range map[string]float64{
				"input_tokens": 30, "output_tokens": 7,
				"cache_creation_input_tokens": 200, "cache_read_input_tokens": 1800,
			} {
				if logged[0][field] != value {
					t.Errorf("logged %s = %v, want %v", field, logged[0][field], value)
				}
			}
			if logged[0]["model"] != "claude-sonnet-4-6" {
				t.Errorf("logged model = %v, want claude-sonnet-4-6", logged[0]["model"])
			}
		})
	}
}
