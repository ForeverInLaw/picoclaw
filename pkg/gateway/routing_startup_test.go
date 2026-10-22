package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestUnavailableStartupPrimaryStillServesRequestThroughConfiguredReserve(t *testing.T) {
	for _, agentOverride := range []bool{false, true} {
		t.Run(fmt.Sprint(agentOverride), func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			require.NoError(
				t,
				auth.SaveAccount(
					&auth.AccountConnection{
						ID:       "disabled",
						Provider: "openai",
						Enabled:  false,
						Credential: &auth.AuthCredential{
							Provider:    "openai",
							AuthMethod:  "oauth",
							AccessToken: "disabled-token",
						},
					},
				),
			)
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "Bearer reserve-key", r.Header.Get("Authorization"))
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"cold reserve answer"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer endpoint.Close()
			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace:         t.TempDir(),
						ContextManager:    "legacy",
						ContextWindow:     64000,
						ModelName:         "primary",
						ModelFallbacks:    []string{"reserve"},
						MaxTokens:         256,
						MaxToolIterations: 3,
					},
				},
				ModelList: []config.ModelConfig{
					{ModelName: "primary", Model: "openai/shared", AuthMethod: "oauth", AccountID: "disabled"},
					{ModelName: "reserve", Model: "openai/shared", APIBase: endpoint.URL, APIKey: "reserve-key"},
				},
			}
			if agentOverride {
				cfg.ModelList = append(
					cfg.ModelList,
					config.ModelConfig{
						ModelName: "healthy-default",
						Model:     "openai/shared",
						APIBase:   endpoint.URL,
						APIKey:    "default-key",
					},
				)
				cfg.Agents.Defaults.ModelName = "healthy-default"
				cfg.Agents.Defaults.ModelFallbacks = []string{}
				cfg.Agents.List = []config.AgentConfig{
					{ID: "main", Default: true},
					{
						ID:        "worker",
						Workspace: t.TempDir(),
						Model:     &config.AgentModelConfig{Primary: "primary", Fallbacks: []string{"reserve"}},
					},
				}
				cfg.Bindings = []config.AgentBinding{{AgentID: "worker", Match: config.BindingMatch{Channel: "cli"}}}
			}
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, config.SaveConfig(path, cfg))
			var err error
			cfg, err = config.LoadConfig(path)
			require.NoError(t, err)
			provider, resolvedModel, err := createStartupProvider(cfg, false)
			require.NoError(t, err)
			if !agentOverride {
				require.Empty(t, resolvedModel)
				require.Equal(t, "primary", cfg.Agents.Defaults.GetModelName())
			} else {
				require.Equal(t, "shared", resolvedModel)
				require.Equal(t, "healthy-default", cfg.Agents.Defaults.GetModelName())
			}
			messageBus := bus.NewMessageBus()
			defer messageBus.Close()
			loop := agent.NewAgentLoop(
				cfg,
				messageBus,
				provider,
				func() (*config.Config, error) { return config.LoadConfig(path) },
			)
			defer loop.Close()
			answer, err := loop.ProcessDirect(t.Context(), "serve using configured reserve", "cold-start")
			require.NoError(t, err)
			require.Equal(t, "cold reserve answer", answer)
			require.Equal(t, 1, calls)
		})
	}
}
