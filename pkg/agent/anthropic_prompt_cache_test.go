package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

// cachedPrefix is the part of a Messages API request body that the explicit
// cache breakpoint covers: the tool definitions and the system blocks up to
// and including the marked one, as raw wire bytes.
type cachedPrefix struct {
	tools    json.RawMessage
	system   []json.RawMessage
	messages []json.RawMessage
}

func parseCachedPrefix(t *testing.T, raw []byte) cachedPrefix {
	t.Helper()
	var body struct {
		Tools    json.RawMessage   `json:"tools"`
		System   []json.RawMessage `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	for i, block := range body.System {
		if bytes.Contains(block, []byte(`"cache_control"`)) {
			return cachedPrefix{tools: body.Tools, system: body.System[:i+1], messages: body.Messages}
		}
	}
	t.Fatalf("no system block carries the cache breakpoint: %s", raw)
	return cachedPrefix{}
}

func TestAnthropicConversationKeepsCachedPrefixByteIdentical(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",
			"stop_reason":"end_turn","content":[{"type":"text","text":"Noted."}],
			"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{{
		ModelName: "claude", Model: "anthropic/claude-sonnet-4.6", APIKey: "sk-ant-test", APIBase: endpoint.URL,
	}}, nil)

	_, err := al.ProcessDirect(t.Context(), "first question", "cache-prefix")
	require.NoError(t, err)
	_, err = al.ProcessDirect(t.Context(), "second question", "cache-prefix")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
	first, second := parseCachedPrefix(t, bodies[0]), parseCachedPrefix(t, bodies[1])
	require.Greater(t, len(first.tools), len("null"), "the agent sends its tool definitions")
	require.Equal(t, string(first.tools), string(second.tools), "tool definitions changed between turns")
	require.Len(t, second.system, len(first.system))
	for i := range first.system {
		require.Equal(t, string(first.system[i]), string(second.system[i]), "static system block %d changed", i)
	}
	require.Greater(t, len(second.messages), len(first.messages))
	for i := range first.messages {
		require.Equal(t, string(first.messages[i]), string(second.messages[i]), "history message %d changed", i)
	}
}
