package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
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
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"once","type":"function","function":{"name":"mock_custom","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
					),
				)
			} else {
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"captured backup"},"finish_reason":"stop"}]}`,
					),
				)
			}
		case "new-primary":
			_, _ = w.Write(
				[]byte(
					`{"choices":[{"message":{"role":"assistant","content":"new priority"},"finish_reason":"stop"}]}`,
				),
			)
		}
	}))
	defer endpoint.Close()
	models := []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: endpoint.URL},
		{ModelName: "backup", Model: "openai/backup", APIBase: endpoint.URL},
		{ModelName: "new-primary", Model: "openai/new-primary", APIBase: endpoint.URL},
	}
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
				_, _ = w.Write(
					[]byte(
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"account B\"}]}]}}\n\n",
					),
				)
			}))
			defer b.Close()
			for _, account := range []struct{ id, url string }{{"a", a.URL}, {"b", b.URL}} {
				require.NoError(
					t,
					auth.SaveAccount(
						&auth.AccountConnection{
							ID:       account.id,
							Provider: "openai",
							Enabled:  true,
							Resource: account.url,
							Scopes:   auth.OpenAIPlanScope,
							Credential: &auth.AuthCredential{
								Provider:    "openai",
								AuthMethod:  "oauth",
								AccessToken: "secret-" + account.id,
							},
						},
					),
				)
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

func TestRoutingSnapshotFallbackAlsoServesForegroundCompaction(t *testing.T) {
	var primaryCalls, summaryCalls, answerCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "backup", input.Model)
		if len(input.Messages) == 1 && input.Messages[0].Role == "user" {
			summaryCalls.Add(1)
		} else {
			answerCalls.Add(1)
		}
		_, _ = w.Write(
			[]byte(
				`{"choices":[{"message":{"role":"assistant","content":"preserved summary and answer"},"finish_reason":"stop"}]}`,
			),
		)
	}))
	defer backup.Close()
	initial, cfg := newSeahorseScenarioLoop(t, &sourceScenarioProvider{})
	initial.Close()
	cfg.Agents.Defaults.Model = "primary"
	cfg.Agents.Defaults.MaxTokens = 123
	cfg.Agents.Defaults.ContextWindow = 12000
	cfg.Agents.Defaults.SummarizeTokenPercent = 100
	cfg.Agents.Defaults.ModelFallbacks = []string{"backup"}
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: primary.URL},
		{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
	}
	provider, _, err := providers.CreateProvider(cfg)
	require.NoError(t, err)
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, config.SaveConfig(path, cfg))
	al.SetRoutingConfigSource(func() (*config.Config, error) { return config.LoadConfig(path) })
	agent := al.registry.GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	for range 8 {
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "user", Content: strings.Repeat("ORIGINAL_EVIDENCE ", 1500)},
		)
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "assistant", Content: "retained answer"})
	}
	answer, err := al.processMessage(t.Context(), selectedInlineMessage("42", "compaction-fallback", "next"))
	require.NoError(t, err)
	require.Contains(t, answer, "preserved summary and answer")
	require.Positive(t, summaryCalls.Load())
	require.Positive(t, answerCalls.Load())
	require.Equal(t, int32(1), primaryCalls.Load())
	require.GreaterOrEqual(t, len(agent.Sessions.GetHistory(key)), 18)
}

func TestSnapshotLongChainReachesFifthCandidateForDirectAndInline(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprint(inline), func(t *testing.T) {
			var mu sync.Mutex
			calls := []string{}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Model string `json:"model"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				mu.Lock()
				calls = append(calls, input.Model)
				mu.Unlock()
				if input.Model != "five" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
					return
				}
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"fifth candidate"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer endpoint.Close()
			models := make([]config.ModelConfig, 0, 5)
			for _, name := range []string{"one", "two", "three", "four", "five"} {
				models = append(
					models,
					config.ModelConfig{
						ModelName: name,
						Model:     "openai/" + name,
						APIBase:   endpoint.URL,
						APIKey:    "key-" + name,
					},
				)
			}
			al, _ := configuredProviderLoop(t, models, []string{"two", "three", "four", "five"})
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, config.SaveConfig(path, al.cfg))
			al.SetRoutingConfigSource(func() (*config.Config, error) { return config.LoadConfig(path) })
			msg := directIngressMessage("walk chain")
			if inline {
				msg = selectedInlineMessage("42", "long-chain", "walk chain")
			}
			answer, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Contains(t, answer, "fifth candidate")
			mu.Lock()
			require.Equal(t, []string{"one", "two", "three", "four", "five"}, calls)
			mu.Unlock()
		})
	}
}

type disableConnectionTool struct {
	mockCustomTool
	calls int
}

func (tool *disableConnectionTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	tool.calls++
	if err := auth.UpdateAccount("a", "Account A", false); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.SilentResult("COMPLETED_ACTION")
}

func TestLiveAccountDisableKeepsCompletedToolResultDuringSameTask(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var aCalls, bCalls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Authorization") == "Bearer secret-a" {
			aCalls.Add(1)
			_, _ = w.Write(
				[]byte(
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"id\":\"fc1\",\"type\":\"function_call\",\"call_id\":\"once\",\"name\":\"mock_custom\",\"namespace\":\"picoclaw\",\"arguments\":\"{}\"}]}}\n\n",
				),
			)
			return
		}
		bCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "COMPLETED_ACTION")
		_, _ = w.Write(
			[]byte(
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r2\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"backup retained completed action\"}]}]}}\n\n",
			),
		)
	}))
	defer endpoint.Close()
	for _, id := range []string{"a", "b"} {
		require.NoError(
			t,
			auth.SaveAccount(
				&auth.AccountConnection{
					ID:       id,
					Provider: "openai",
					Enabled:  true,
					Resource: endpoint.URL,
					Scopes:   auth.OpenAIPlanScope,
					Credential: &auth.AuthCredential{
						Provider:    "openai",
						AuthMethod:  "oauth",
						AccessToken: "secret-" + id,
					},
				},
			),
		)
	}
	al, _ := configuredProviderLoop(
		t,
		[]config.ModelConfig{
			{ModelName: "a", Model: "openai/shared", AuthMethod: "oauth", AccountID: "a"},
			{ModelName: "b", Model: "openai/shared", AuthMethod: "oauth", AccountID: "b"},
		},
		[]string{"b"},
	)
	tool := &disableConnectionTool{}
	al.RegisterTool(tool)
	answer, err := al.ProcessDirect(t.Context(), "perform action then finish", "live-disable")
	require.NoError(t, err)
	require.Equal(t, "backup retained completed action", answer)
	require.Equal(t, 1, tool.calls)
	require.Equal(t, int32(1), aCalls.Load())
	require.Equal(t, int32(1), bCalls.Load())
}
