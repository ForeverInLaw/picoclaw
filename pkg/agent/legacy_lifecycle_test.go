package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// The provider observes cancellation and deliberately waits for release, making
// shutdown ordering observable without inspecting manager fields.
type blockedLegacySummaryProvider struct {
	summaryCalls                         atomic.Int32
	honorCancellation                    bool
	returnOnce                           sync.Once
	lastInput                            []providers.Message
	started, canceled, release, returned chan struct{}
	startOnce, cancelOnce, releaseOnce   sync.Once
	mu                                   sync.Mutex
	active, closed, closedWhileActive    bool
}

func TestLegacySummaryUsesOwningAgentsExactConfiguredProviderAndModel(t *testing.T) {
	var mu sync.Mutex
	var keys, models []string
	started := make(chan struct{})
	var startedOnce sync.Once
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if len(request.Messages) == 1 && request.Messages[0].Role == "user" {
			mu.Lock()
			keys = append(keys, r.Header.Get("Authorization"))
			models = append(models, request.Model)
			mu.Unlock()
			startedOnce.Do(func() { close(started) })
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer endpoint.Close()
	base, cfg, mb, _, cleanup := newTestAgentLoop(t)
	base.Close()
	mb.Close()
	cleanup()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "main-model"
	cfg.Agents.Defaults.SummarizeMessageThreshold = 4
	cfg.Agents.Defaults.SummarizeTokenPercent = 1000
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "main-model", Model: "openai/actual-main", APIKey: "main-key", APIBase: endpoint.URL},
		{ModelName: "reviewer-model", Model: "openai/actual-reviewer", APIKey: "reviewer-key-A", APIBase: endpoint.URL},
		{ModelName: "reviewer-spare", Model: "openai/actual-reviewer", APIKey: "reviewer-key-B", APIBase: endpoint.URL},
	}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "reviewer", Workspace: t.TempDir(), Model: &config.AgentModelConfig{Primary: "reviewer-model"}},
	}
	cfg.Bindings = []config.AgentBinding{{AgentID: "reviewer", Match: config.BindingMatch{Channel: "telegram"}}}
	provider, _, err := providers.CreateProvider(cfg)
	require.NoError(t, err)
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, provider)
	t.Cleanup(func() { al.Close(); messageBus.Close() })
	for range 3 {
		_, turnErr := al.ProcessDirectWithChannel(
			t.Context(),
			"source evidence",
			"agent:reviewer:legacy",
			"telegram",
			"42",
		)
		require.NoError(t, turnErr)
	}
	awaitSignal(t, started, "configured legacy summary")
	al.Close()
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, keys, "legacy summary must reach the configured HTTP model")
	for i, key := range keys {
		require.Equal(t, "Bearer reviewer-key-A", key)
		require.Equal(t, "actual-reviewer", models[i])
	}
}

func TestLegacyModelSwitchRetainsCapturedProviderUntilSummaryJoins(t *testing.T) {
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"next answer"}}]}`))
	}))
	defer next.Close()
	p := newBlockedLegacySummaryProvider()
	al, cfg := blockedLegacyLoop(t, p)
	cfg.ModelList = append(
		cfg.ModelList,
		config.ModelConfig{ModelName: "next", Model: "openai/next-model", APIKey: "next-key", APIBase: next.URL},
	)
	_, err := al.ProcessDirect(t.Context(), "/switch model to next", "legacy-lifecycle")
	require.NoError(t, err)
	p.mu.Lock()
	closed, early := p.closed, p.closedWhileActive
	p.mu.Unlock()
	require.False(t, closed, "model switch must retain the captured summary provider")
	require.False(t, early)
	p.releaseWork()
	awaitSignal(t, p.returned, "old model summary completion")
	al.Close()
	p.mu.Lock()
	closed, early = p.closed, p.closedWhileActive
	p.mu.Unlock()
	require.True(t, closed, "retained provider must close after summary joins")
	require.False(t, early)
}

func newBlockedLegacySummaryProvider() *blockedLegacySummaryProvider {
	return &blockedLegacySummaryProvider{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

func (p *blockedLegacySummaryProvider) GetDefaultModel() string { return "test-model" }

func (p *blockedLegacySummaryProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if len(messages) != 1 || messages[0].Role != "user" {
		p.mu.Lock()
		p.lastInput = append([]providers.Message(nil), messages...)
		p.mu.Unlock()
		return &providers.LLMResponse{Content: "answer"}, nil
	}
	p.mu.Lock()
	p.active = true
	p.mu.Unlock()
	p.startOnce.Do(func() { close(p.started) })
	p.summaryCalls.Add(1)
	select {
	case <-ctx.Done():
		p.cancelOnce.Do(func() { close(p.canceled) })
		<-p.release
	case <-p.release:
	}
	p.mu.Lock()
	p.active = false
	p.mu.Unlock()
	p.returnOnce.Do(func() { close(p.returned) })
	if p.honorCancellation && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "OLD_LATE_SUMMARY"}, nil
}

func TestLegacyCanceledSummaryDoesNotRetryProvider(t *testing.T) {
	p := newBlockedLegacySummaryProvider()
	p.honorCancellation = true
	al, _ := blockedLegacyLoop(t, p)
	closed := make(chan struct{})
	go func() { al.Close(); close(closed) }()
	awaitSignal(t, p.canceled, "summary cancellation")
	p.releaseWork()
	awaitSignal(t, closed, "canceled summary join")
	require.Equal(t, int32(1), p.summaryCalls.Load(), "canceled maintenance must not call the provider again")
}

func TestLegacyResetCancelsSummaryWithoutWaitingAndRejectsLatePublication(t *testing.T) {
	p := newBlockedLegacySummaryProvider()
	al, _ := blockedLegacyLoop(t, p)
	reset := make(chan error, 1)
	go func() {
		reply, err := al.ProcessDirect(t.Context(), "/clear", "legacy-lifecycle")
		if err == nil && !strings.Contains(reply, "cleared") {
			err = fmt.Errorf("unexpected reset reply: %s", reply)
		}
		reset <- err
	}()
	select {
	case err := <-reset:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("reset waited for blocked legacy summary")
	}
	awaitSignal(t, p.canceled, "legacy reset cancellation")
	p.releaseWork()
	awaitSignal(t, p.returned, "late legacy summary response")
	_, err := al.ProcessDirect(t.Context(), "fresh conversation", "legacy-lifecycle")
	require.NoError(t, err)
	p.mu.Lock()
	input := conversationInput(p.lastInput)
	p.mu.Unlock()
	require.NotContains(t, input, "OLD_DIALOG")
	require.NotContains(t, input, "OLD_LATE_SUMMARY")
}

func (p *blockedLegacySummaryProvider) releaseWork() { p.releaseOnce.Do(func() { close(p.release) }) }

func (p *blockedLegacySummaryProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.closedWhileActive = p.active
}

func blockedLegacyLoop(t *testing.T, p *blockedLegacySummaryProvider) (*AgentLoop, *config.Config) {
	t.Helper()
	base, cfg, mb, _, cleanup := newTestAgentLoop(t)
	base.Close()
	mb.Close()
	cleanup()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.SummarizeMessageThreshold = 4
	cfg.Agents.Defaults.SummarizeTokenPercent = 1000
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, p)
	t.Cleanup(func() { al.Close(); messageBus.Close() })
	t.Cleanup(p.releaseWork)
	for range 3 {
		_, err := al.ProcessDirect(t.Context(), "OLD_DIALOG", "legacy-lifecycle")
		require.NoError(t, err)
	}
	awaitSignal(t, p.started, "legacy summary start")
	return al, cfg
}

func TestLegacyCloseCancelsAndJoinsSummaryBeforeProviderAndStorageClose(t *testing.T) {
	p := newBlockedLegacySummaryProvider()
	al, _ := blockedLegacyLoop(t, p)
	closed := make(chan struct{})
	go func() { al.Close(); close(closed) }()
	select {
	case <-p.canceled:
	case <-closed:
		t.Fatal("Close returned before canceling and joining legacy summary")
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel legacy summary")
	}
	select {
	case <-closed:
		t.Fatal("Close returned while legacy provider was still working")
	default:
	}
	p.releaseWork()
	awaitSignal(t, closed, "legacy manager close")
	awaitSignal(t, p.returned, "legacy provider completion")
	p.Close()
	p.mu.Lock()
	closedEarly := p.closedWhileActive
	p.mu.Unlock()
	require.False(t, closedEarly, "provider closed before summary joined")
}

func TestLegacyReloadCancelsAndJoinsSummaryBeforeClosingOldProvider(t *testing.T) {
	p := newBlockedLegacySummaryProvider()
	al, cfg := blockedLegacyLoop(t, p)
	reloaded := make(chan error, 1)
	go func() { reloaded <- al.ReloadProviderAndConfig(t.Context(), &recordingProvider{}, cfg) }()
	select {
	case <-p.canceled:
	case err := <-reloaded:
		t.Fatalf("reload returned before canceling legacy summary: %v", err)
	case <-time.After(time.Second):
		t.Fatal("reload did not cancel legacy summary")
	}
	select {
	case err := <-reloaded:
		t.Fatalf("reload returned while summary was working: %v", err)
	default:
	}
	p.releaseWork()
	require.NoError(t, <-reloaded)
	p.mu.Lock()
	closed, early := p.closed, p.closedWhileActive
	p.mu.Unlock()
	require.True(t, closed, "reload must close its old provider after joining maintenance")
	require.False(t, early)
}
