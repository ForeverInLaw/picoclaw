package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// Key pools are read per request, so their metadata needs no reload; their
// keys and secrets still do.
func TestRoutingOnlyChangeTreatsKeyPoolMetadataAsRouting(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Orgs", Keys: []config.KeyPoolKey{
		{ID: "a", Label: "First", Enabled: true, APIKey: "sk-ant-a"},
	}}}
	edits := map[string]struct {
		edit    func(*config.KeyPool)
		routing bool
	}{
		"pool name":         {func(p *config.KeyPool) { p.Name = "Organizations" }, true},
		"label":             {func(p *config.KeyPool) { p.Keys[0].Label = "Main" }, true},
		"return to service": {func(p *config.KeyPool) { p.Keys[0].ReturnedAt = time.Now() }, true},
		"budget":            {func(p *config.KeyPool) { p.Keys[0].MonthlyBudgetUSD = 200 }, true},
		"reset day":         {func(p *config.KeyPool) { p.Keys[0].ResetDay = 15 }, true},
		"secret":            {func(p *config.KeyPool) { p.Keys[0].APIKey = "sk-ant-new" }, false},
		"enabled":           {func(p *config.KeyPool) { p.Keys[0].Enabled = false }, false},
		"added key": {func(p *config.KeyPool) {
			p.Keys = append(p.Keys, config.KeyPoolKey{ID: "b", Enabled: true, APIKey: "sk-ant-b"})
		}, false},
		"removed key": {func(p *config.KeyPool) { p.Keys = nil }, false},
	}
	for name, tc := range edits {
		next := *cfg
		next.KeyPools = []config.KeyPool{cfg.KeyPools[0]}
		next.KeyPools[0].Keys = append([]config.KeyPoolKey(nil), cfg.KeyPools[0].Keys...)
		tc.edit(&next.KeyPools[0])
		require.Equal(t, tc.routing, routingOnlyChange(cfg, &next), name)
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
