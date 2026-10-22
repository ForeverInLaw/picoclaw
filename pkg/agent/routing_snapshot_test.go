package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestRequestsCaptureRoutingChangesAndKeepSuccessfulBackupForTools(t *testing.T) {
	var mu sync.Mutex
	calls := []string{}
	started, release := make(chan struct{}), make(chan struct{})
	var backupCalls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		mu.Lock()
		calls = append(calls, input.Model)
		mu.Unlock()
		switch input.Model {
		case "primary":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"message":"service unavailable"}}`))
		case "backup":
			if backupCalls.Add(1) == 1 {
				close(started)
				<-release
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"once","type":"function","function":{"name":"mock_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"captured backup"},"finish_reason":"stop"}]}`))
			}
		case "new-primary":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"new priority"},"finish_reason":"stop"}]}`))
		}
	}))
	defer endpoint.Close()
	models := []config.ModelConfig{{ModelName: "primary", Model: "openai/primary", APIBase: endpoint.URL}, {ModelName: "backup", Model: "openai/backup", APIBase: endpoint.URL}, {ModelName: "new-primary", Model: "openai/new-primary", APIBase: endpoint.URL}}
	al, _ := configuredProviderLoop(t, models, []string{"backup"})
	al.RegisterTool(&mockCustomTool{})
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, config.SaveConfig(path, al.cfg))
	al.SetRoutingConfigSource(func() (*config.Config, error) { return config.LoadConfig(path) })
	original := al.registry.GetDefaultAgent()
	sessions, manager := original.Sessions, original.ContextManager
	done := make(chan string, 1)
	go func() {
		answer, err := al.ProcessDirect(t.Context(), "use a tool", "snapshot")
		if err != nil {
			done <- err.Error()
		} else {
			done <- answer
		}
	}()
	<-started
	changed, err := config.LoadConfig(path)
	require.NoError(t, err)
	changed.Agents.Defaults.ModelName = "new-primary"
	changed.Agents.Defaults.ModelFallbacks = []string{}
	require.NoError(t, config.SaveConfig(path, changed))
	close(release)
	require.Equal(t, "captured backup", <-done)
	next, err := al.ProcessDirect(t.Context(), "next request", "snapshot")
	require.NoError(t, err)
	require.Equal(t, "new priority", next)
	mu.Lock()
	require.Equal(t, []string{"primary", "backup", "backup", "new-primary"}, calls)
	mu.Unlock()
	require.Same(t, sessions, al.registry.GetDefaultAgent().Sessions)
	require.Same(t, manager, al.registry.GetDefaultAgent().ContextManager)
}

func TestAccountFailuresSuppressAliasesWithoutRevokingPolicyDeniedAccounts(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		code   string
		wantA  int32
	}{
		{"unauthorized", 401, "invalid_token", 1},
		{"quota", 429, "subscription_sharing_usage_limit_exceeded", 1},
		{"policy", 403, "permission_denied", 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			var aCalls atomic.Int32
			a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				aCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(scenario.status)
				_, _ = w.Write([]byte(`{"error":{"code":"` + scenario.code + `","message":"unavailable"}}`))
			}))
			defer a.Close()
			b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"account B\"}]}]}}\n\n"))
			}))
			defer b.Close()
			for _, account := range []struct{ id, url string }{{"a", a.URL}, {"b", b.URL}} {
				require.NoError(t, auth.SaveAccount(&auth.AccountConnection{ID: account.id, Provider: "openai", Enabled: true, Resource: account.url, Scopes: auth.OpenAIPlanScope, Credential: &auth.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "secret-" + account.id}}))
			}
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{ModelName: "a-first", Model: "openai/shared", AuthMethod: "oauth", AccountID: "a"},
				{ModelName: "a-alias", Model: "openai/shared", AuthMethod: "oauth", AccountID: "a"},
				{ModelName: "b", Model: "openai/shared", AuthMethod: "oauth", AccountID: "b"},
			}, []string{"a-alias", "b"})
			answer, err := al.ProcessDirect(t.Context(), "serve via eligible connection", "account-health")
			require.NoError(t, err)
			require.Equal(t, "account B", answer)
			require.Equal(t, scenario.wantA, aCalls.Load())
		})
	}
}
