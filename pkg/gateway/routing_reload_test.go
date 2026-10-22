package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice"
)

func TestRoutingOnlyChangeRetainsTranscriberDependencies(t *testing.T) {
	for _, selection := range []string{"audio", "model-id:voice", ""} {
		t.Run(selection, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Voice.ModelName = selection
			cfg.ModelList = []config.ModelConfig{
				{ID: "chat", ModelName: "chat", Model: "openai/chat", APIKey: "chat-key"},
				{ID: "voice", ModelName: "audio", Model: "groq/whisper", APIKey: "audio-key"},
			}
			for _, field := range []string{"chat", "key", "base", "model", "removed"} {
				next := *cfg
				next.ModelList = append([]config.ModelConfig(nil), cfg.ModelList...)
				switch field {
				case "chat":
					next.ModelList[0].APIKey = "new-chat-key"
					next.Agents.Defaults.ModelName = "chat"
				case "key":
					next.ModelList[1].APIKey = "new-audio-key"
				case "base":
					next.ModelList[1].APIBase = "https://new.example/v1"
				case "model":
					next.ModelList[1].Model = "groq/new-audio"
				case "removed":
					next.ModelList = next.ModelList[:1]
				}
				require.Equal(t, field == "chat", routingOnlyChange(cfg, &next), field)
			}
		})
	}
}

func TestTranscriberReloadUsesEditedModelEndpointAndCredentials(t *testing.T) {
	calls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "Bearer new-key", r.Header.Get("Authorization"))
		var input struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "new-audio", input.Model)
		_, _ = w.Write(
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"transcribed"},"finish_reason":"stop"}]}`),
		)
	}))
	defer endpoint.Close()
	cfg := config.DefaultConfig()
	cfg.Voice.ModelName = "audio"
	cfg.ModelList = []config.ModelConfig{
		{ID: "voice", ModelName: "audio", Model: "openai/old-audio", APIKey: "old-key"},
	}
	next := *cfg
	next.ModelList = []config.ModelConfig{
		{ID: "voice", ModelName: "audio", Model: "openai/new-audio", APIKey: "new-key", APIBase: endpoint.URL},
	}
	require.False(t, routingOnlyChange(cfg, &next))
	transcriber := voice.DetectTranscriber(&next)
	require.NotNil(t, transcriber)
	path := filepath.Join(t.TempDir(), "sample.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio"), 0o600))
	response, err := transcriber.Transcribe(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, "transcribed", response.Text)
	require.Equal(t, 1, calls)
}
