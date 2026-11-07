package providers

import (
	"net/http"
	"testing"
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
