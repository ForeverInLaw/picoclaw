package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func configuredProviderLoop(
	t *testing.T,
	models []config.ModelConfig,
	fallbacks []string,
) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	initial, cfg, _, _, cleanup := newTestAgentLoop(t)
	initial.Close()
	cfg.Agents.Defaults.Model = models[0].ModelName
	cfg.Agents.Defaults.ModelFallbacks = fallbacks
	cfg.ModelList = models
	provider, _, err := providers.CreateProvider(cfg)
	require.NoError(t, err)
	mb := bus.NewMessageBus()
	al := NewAgentLoop(cfg, mb, provider)
	t.Cleanup(func() {
		al.Close()
		mb.Close()
		cleanup()
	})
	return al, mb
}

func startConfiguredProviderLoop(
	t *testing.T,
	models []config.ModelConfig,
	fallbacks []string,
) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	al, mb := configuredProviderLoop(t, models, fallbacks)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	return al, mb
}

func TestRunFallbackSeparatesSameModelConfigurations(t *testing.T) {
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		assertConfiguredRequest(t, r, "primary-key", "shared")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		assertConfiguredRequest(t, r, "backup-key", "shared")
		_, _ = w.Write(
			[]byte(
				`{"choices":[{"message":{"role":"assistant","content":"backup recovered"},"finish_reason":"stop"}]}`,
			),
		)
	}))
	defer backup.Close()
	_, mb := startConfiguredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/shared", APIKey: "primary-key", APIBase: primary.URL},
		{ModelName: "backup", Model: "openai/shared", APIKey: "backup-key", APIBase: backup.URL},
	}, []string{"backup"})
	publishIncoming(t, mb, directIngressMessage("recover with the separately configured endpoint"))
	out := finalOutbound(t, mb)
	require.Equal(t, "backup recovered", out.Content)
	require.Equal(t, int32(1), primaryCalls.Load())
	require.Equal(t, int32(1), backupCalls.Load())
}

func TestRunModelDefaultChainPreservesMultiKeyConfigurations(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer first-key":
			firstCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
		case "Bearer second-key":
			secondCalls.Add(1)
			_, _ = w.Write(
				[]byte(
					`{"choices":[{"message":{"role":"assistant","content":"second key recovered"},"finish_reason":"stop"}]}`,
				),
			)
		default:
			t.Error("unexpected credential selection")
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer endpoint.Close()
	models := config.ExpandMultiKeyModels(
		[]config.ModelConfig{
			{
				ModelName: "multi",
				Model:     "openai/shared",
				APIBase:   endpoint.URL,
				APIKeys:   []string{"first-key", "second-key"},
			},
		},
	)
	// Expansion places the generated fallback entry before the primary alias.
	for i := range models {
		if models[i].ModelName == "multi" {
			models[0], models[i] = models[i], models[0]
			break
		}
	}
	for _, tc := range []struct {
		name      string
		fallbacks []string
		recovered bool
	}{
		{"model default", nil, true},
		{"explicit empty", []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstCalls.Store(0)
			secondCalls.Store(0)
			_, mb := startConfiguredProviderLoop(t, models, tc.fallbacks)
			publishIncoming(t, mb, directIngressMessage("recover using the configured default key chain"))
			out := finalOutbound(t, mb)
			require.Equal(t, int32(1), firstCalls.Load())
			if tc.recovered {
				require.Equal(t, "second key recovered", out.Content)
				require.Equal(t, int32(1), secondCalls.Load())
			} else {
				require.Contains(t, out.Content, "API key")
				require.Zero(t, secondCalls.Load())
			}
		})
	}
}

func TestRunProviderErrorsAreActionableAndSanitized(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		message string
		hint    string
	}{
		{"authentication", http.StatusUnauthorized, "invalid API key tiny", "API key"},
		{"model endpoint", http.StatusNotFound, "model_not_found tiny", "api_base"},
		{"invalid request", http.StatusBadRequest, "invalid model tiny", "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": tc.message}})
			}))
			defer endpoint.Close()
			al, mb := startConfiguredProviderLoop(
				t,
				[]config.ModelConfig{
					{ModelName: "configured", Model: "openai/model", APIBase: endpoint.URL, APIKey: "tiny"},
				},
				nil,
			)
			al.GetConfig().Tools.FilterSensitiveData = false
			sub := al.SubscribeEvents(32)
			defer al.UnsubscribeEvents(sub.ID)
			publishIncoming(t, mb, directIngressMessage("try the configured model"))
			out := finalOutbound(t, mb)
			require.Contains(t, out.Content, tc.hint)
			require.NotContains(t, out.Content, "tiny")
			require.Equal(t, int32(1), calls.Load())
			for {
				select {
				case event := <-sub.C:
					if event.Kind == EventKindError {
						require.NotContains(t, event.Payload.(ErrorPayload).Message, "tiny")
						return
					}
				default:
					t.Fatal("missing provider error event")
					return
				}
			}
		})
	}
}

func TestRunRoundRobinKeepsFactorySelectedConfiguration(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	endpoint := func(key string, calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			assertConfiguredRequest(t, r, key, "shared")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
		}))
	}
	first := endpoint("first-key", &firstCalls)
	defer first.Close()
	second := endpoint("second-key", &secondCalls)
	defer second.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := "first endpoint"
		if r.Header.Get("Authorization") == "Bearer second-backup-key" {
			answer = "second endpoint"
		} else {
			require.Equal(t, "Bearer first-backup-key", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}}})
	}))
	defer backup.Close()
	models := []config.ModelConfig{
		{
			ModelName: "round-robin",
			Model:     "openai/shared",
			APIBase:   first.URL,
			APIKey:    "first-key",
			Fallbacks: []string{"first-backup"},
		},
		{
			ModelName: "round-robin",
			Model:     "openai/shared",
			APIBase:   second.URL,
			APIKey:    "second-key",
			Fallbacks: []string{"second-backup"},
		},
		{ModelName: "first-backup", Model: "openai/shared", APIBase: backup.URL, APIKey: "first-backup-key"},
		{ModelName: "second-backup", Model: "openai/shared", APIBase: backup.URL, APIKey: "second-backup-key"},
	}
	for i := 0; i < 2; i++ {
		beforeFirst := firstCalls.Load()
		_, mb := startConfiguredProviderLoop(t, models, nil)
		publishIncoming(t, mb, directIngressMessage("use the selected configured client"))
		out := finalOutbound(t, mb)
		if firstCalls.Load() > beforeFirst {
			require.Equal(t, "first endpoint", out.Content)
		} else {
			require.Equal(t, "second endpoint", out.Content)
		}
	}
	require.Equal(t, int32(1), firstCalls.Load())
	require.Equal(t, int32(1), secondCalls.Load())
}

func TestRunSameModelFallbackRetryRetainsEndpointAndCredential(t *testing.T) {
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Idle socket readers would prevent synctest's clock from advancing.
		w.Header().Set("Connection", "close")
		primaryCalls.Add(1)
		assertConfiguredRequest(t, r, "primary-key", "shared")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable primary-key"}}`))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		assertConfiguredRequest(t, r, "backup-key", "shared")
		if backupCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable backup-key"}}`))
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"choices":[{"message":{"role":"assistant","content":"backup retry recovered"},"finish_reason":"stop"}]}`,
			),
		)
	}))
	defer backup.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/shared", APIKey: "primary-key", APIBase: primary.URL},
		{ModelName: "backup", Model: "openai/shared", APIKey: "backup-key", APIBase: backup.URL},
	}, []string{"backup"})
	sub := al.SubscribeEvents(32)
	defer al.UnsubscribeEvents(sub.ID)
	synctest.Test(t, func(t *testing.T) {
		answer, err := al.ProcessDirect(t.Context(), "recover with the remaining shared attempt", "same-model-retry")
		require.NoError(t, err)
		require.Equal(t, "backup retry recovered", answer)
	})
	require.Equal(t, int32(1), primaryCalls.Load())
	require.Equal(t, int32(2), backupCalls.Load())
	for {
		select {
		case event := <-sub.C:
			if event.Kind == EventKindLLMRetry {
				errorText := event.Payload.(LLMRetryPayload).Error
				require.NotContains(t, errorText, "primary-key")
				require.NotContains(t, errorText, "backup-key")
			}
		default:
			return
		}
	}
}

func assertConfiguredRequest(t *testing.T, request *http.Request, key, model string) {
	t.Helper()
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	if body.Model != model || request.Header.Get("Authorization") != "Bearer "+key {
		t.Errorf(
			"model or credential crossed configuration: model=%q authorization=%q",
			body.Model,
			request.Header.Get("Authorization"),
		)
	}
}
