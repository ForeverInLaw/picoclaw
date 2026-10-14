package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestProcessDirectProviderFailureIsSafeAndRetainsHTTPMetadata(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid API key tiny; untrusted upstream text"}}`))
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL},
	}, nil)
	al.cfg.Tools.FilterSensitiveData = false
	al.cfg.ModelList[0].APIKeys = []string{"key"}
	_, err := al.ProcessDirect(t.Context(), "check selected endpoint", "direct-error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Authentication failed")
	require.NotContains(t, err.Error(), "tiny")
	require.NotContains(t, err.Error(), "key", "short APIKeys must be protected even within guidance")
	require.NotContains(t, err.Error(), "untrusted upstream text")
	var source *protocoltypes.HTTPError
	require.True(t, errors.As(err, &source), "public error must retain the typed source")
	require.Equal(t, http.StatusUnauthorized, source.StatusCode)
	require.Contains(t, source.Error(), "tiny", "original diagnostic is available only by explicit unwrapping")
	failure := providers.ClassifyError(err, "openai", "shared")
	require.NotNil(t, failure)
	require.Equal(t, providers.FailoverAuth, failure.Reason)
	require.Equal(t, 7*time.Second, failure.RetryAfter)
	if strings.Contains(err.Error(), endpoint.URL) {
		t.Fatal("public error leaked the raw endpoint")
	}
}

func TestProcessDirectMalformedProviderResponseNeverPrintsUntrustedBody(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>tiny untrusted response payload</body></html>`))
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(
		t,
		[]config.ModelConfig{{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL}},
		nil,
	)
	al.cfg.Tools.FilterSensitiveData = false
	logPath := filepath.Join(t.TempDir(), "malformed.jsonl")
	require.NoError(t, logger.EnableFileLogging(logPath))
	t.Cleanup(logger.DisableFileLogging)
	_, err := al.ProcessDirect(t.Context(), "read malformed response", "malformed-error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "selected provider")
	require.NotContains(t, err.Error(), "tiny")
	require.NotContains(t, err.Error(), "untrusted response payload")
	logs, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.NotContains(t, string(logs), "untrusted response payload")
}

func TestProcessDirectProviderCannotSpoofTrustedEmptyAnswerFailure(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": errEmptyAnswer.Error() + " tiny untrusted body"},
		})
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL},
	}, nil)
	al.cfg.Tools.FilterSensitiveData = false
	_, err := al.ProcessDirect(t.Context(), "read rejected request", "spoofed-empty-answer")
	require.Error(t, err)
	require.NotErrorIs(t, err, errEmptyAnswer)
	require.Contains(t, err.Error(), "provider rejected")
	require.NotContains(t, err.Error(), "no answer")
	require.NotContains(t, err.Error(), "untrusted body")
	require.NotContains(t, err.Error(), "tiny")
}

func TestProcessDirectCanceledProviderRetainsCancellationWithoutRawEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(
		t,
		[]config.ModelConfig{
			{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL + "?access_token=tiny"},
		},
		nil,
	)
	al.cfg.Tools.FilterSensitiveData = false
	_, err := al.ProcessDirect(ctx, "cancel this request", "cancel-public-error")
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "canceled")
	require.NotContains(t, err.Error(), "tiny")
	require.NotContains(t, err.Error(), endpoint.URL)
	require.Nil(t, providers.ClassifyError(err, "openai", "shared"))
}

func TestSeahorseBackgroundCompactionFailureLogsSafeProviderGuidance(t *testing.T) {
	var compactions atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if len(request.Messages) == 1 && request.Messages[0].Role == "user" {
			compactions.Add(1)
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid API key tiny xy; untrusted compact body"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer endpoint.Close()
	base, cfg, mb, _, cleanup := newTestAgentLoop(t)
	base.Close()
	mb.Close()
	cleanup()
	cfg.Agents.Defaults.ContextManager = "seahorse"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextWindow = 9000
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Agents.Defaults.SummarizeTokenPercent = 20
	cfg.Agents.Defaults.Model = "probe"
	cfg.Tools.FilterSensitiveData = false
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIKeys: []string{"xy"}, APIBase: endpoint.URL},
	}
	provider, _, err := providers.CreateProvider(cfg)
	require.NoError(t, err)
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, provider)
	t.Cleanup(func() { al.Close(); messageBus.Close() })
	logPath := filepath.Join(t.TempDir(), "compaction.jsonl")
	require.NoError(t, logger.EnableFileLogging(logPath))
	t.Cleanup(logger.DisableFileLogging)
	for range 5 {
		_, turnErr := al.ProcessDirect(t.Context(), strings.Repeat("context evidence ", 80), "background-warning")
		require.NoError(t, turnErr)
	}
	require.Eventually(t, func() bool {
		logs, readErr := os.ReadFile(logPath)
		return readErr == nil && strings.Contains(string(logs), "Context compaction failed")
	}, 2*time.Second, 10*time.Millisecond, "expected a background compaction failure warning")
	require.Positive(t, compactions.Load())
	logs, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logs), "Authentication failed")
	for _, secret := range []string{"tiny", "xy", "untrusted compact body"} {
		require.NotContains(t, string(logs), secret)
	}
}

func TestProcessDirectSeahorseCompactionFailureRetainsSourceWithoutPrintingBody(t *testing.T) {
	var contextExceeded atomic.Bool
	var compactions atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if len(request.Messages) == 1 && request.Messages[0].Role == "user" {
			compactions.Add(1)
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"private tiny compaction failure"}}`))
			return
		}
		if request.Messages[len(request.Messages)-1].Content == "trigger compaction" &&
			contextExceeded.CompareAndSwap(false, true) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded tiny"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer endpoint.Close()
	initial, cfg := newSeahorseScenarioLoop(t, &recordingProvider{})
	initial.Close()
	cfg.Agents.Defaults.Model = "probe"
	cfg.Agents.Defaults.ContextWindow = 30000
	cfg.Agents.Defaults.SummarizeTokenPercent = 1000
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL},
	}
	cfg.Tools.FilterSensitiveData = false
	provider, _, err := providers.CreateProvider(cfg)
	require.NoError(t, err)
	mb := bus.NewMessageBus()
	al := NewAgentLoop(cfg, mb, provider)
	t.Cleanup(func() { al.Close(); mb.Close() })
	for range 6 {
		_, turnErr := al.ProcessDirect(t.Context(), strings.Repeat("source evidence ", 200), "compact-public-error")
		require.NoError(t, turnErr)
	}
	_, err = al.ProcessDirect(t.Context(), "trigger compaction", "compact-public-error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Authentication failed")
	require.NotContains(t, err.Error(), "tiny")
	require.NotContains(t, err.Error(), "private")
	var source *protocoltypes.HTTPError
	require.ErrorAs(t, err, &source)
	require.Equal(t, 401, source.StatusCode)
	require.Equal(t, int32(1), compactions.Load())
}

func TestContextRecoveryWarningDoesNotExposeProviderBodyOrShortAPIKeys(t *testing.T) {
	var failed atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		last := request.Messages[len(request.Messages)-1].Content
		if last == "trigger compression" && failed.CompareAndSwap(false, true) {
			w.WriteHeader(400)
			_, _ = w.Write(
				[]byte(`{"error":{"message":"maximum context length exceeded tiny xy; untrusted upstream text"}}`),
			)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer endpoint.Close()
	al, _ := configuredProviderLoop(
		t,
		[]config.ModelConfig{{ModelName: "probe", Model: "openai/shared", APIKey: "tiny", APIBase: endpoint.URL}},
		nil,
	)
	al.cfg.Tools.FilterSensitiveData = false
	al.cfg.ModelList[0].APIKeys = []string{"xy"}
	logPath := filepath.Join(t.TempDir(), "errors.jsonl")
	require.NoError(t, logger.EnableFileLogging(logPath))
	t.Cleanup(logger.DisableFileLogging)
	for range 6 {
		_, err := al.ProcessDirect(t.Context(), "seed context", "recovery-warning")
		require.NoError(t, err)
	}
	_, err := al.ProcessDirect(t.Context(), "trigger compression", "recovery-warning")
	require.NoError(t, err)
	logs, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logs), "Context window error detected")
	for _, secret := range []string{"tiny", "xy", "untrusted upstream text"} {
		require.NotContains(t, string(logs), secret)
	}
}
