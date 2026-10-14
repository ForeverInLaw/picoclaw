package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSeahorseCompactsActiveContextWithoutDeletingCanonicalTranscript(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	al.Close()
	// Settings go through the real config/constructor boundary.
	var settings config.Config
	if err := settings.UnmarshalJSON([]byte(`{"agents":{"defaults":{"context_manager":"seahorse"}}}`)); err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults = settings.Agents.Defaults
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Agents.Defaults.ContextWindow = 7000
	provider := &recordingProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	for i := 0; i < 24; i++ {
		if _, err := al.processMessage(
			t.Context(),
			selectedInlineMessage("42", "target", strings.Repeat("source detail ", 70)),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := al.processMessage(
		t.Context(),
		selectedInlineMessage("42", "target", "Continue after compaction"),
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(provider.lastMessages), "summary") {
		t.Fatal("source-linked context was not compressed")
	}
	history := al.GetRegistry().GetDefaultAgent().Sessions.GetHistory(inlineConversationKey("main", "42"))
	if len(history) != 50 {
		t.Fatalf("canonical transcript was truncated: %d", len(history))
	}
}

// Requests and tool results are observed at the provider boundary, including
// concurrent maintenance calls; scenarios never inspect manager internals.
type sourceScenarioProvider struct {
	mu             sync.Mutex
	tool           string
	args           map[string]any
	grepOutput     string
	expandOutput   string
	lastInput      []providers.Message
	summaryPrompts []string
}

func (p *sourceScenarioProvider) GetDefaultModel() string { return "test-model" }

func (p *sourceScenarioProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(messages) == 1 && messages[0].Role == "user" {
		p.summaryPrompts = append(p.summaryPrompts, messages[0].Content)
		return &providers.LLMResponse{Content: "COMPACT_SUMMARY"}, nil
	}
	p.lastInput = append([]providers.Message(nil), messages...)
	last := messages[len(messages)-1]
	if last.Role == "user" && last.Content == "query sources" {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{ID: "lookup", Name: p.tool, Arguments: p.args}},
		}, nil
	}
	if last.Role == "tool" {
		if strings.Contains(last.Content, `"total_messages"`) {
			p.grepOutput = last.Content
			var found struct {
				Messages []struct {
					ID string `json:"id"`
				} `json:"messages"`
				Summaries []struct {
					ID string `json:"id"`
				} `json:"summaries"`
			}
			if err := json.Unmarshal([]byte(last.Content), &found); err != nil {
				return nil, err
			}
			ids := []string{}
			for _, match := range found.Messages {
				ids = append(ids, match.ID)
			}
			for _, match := range found.Summaries {
				ids = append(ids, match.ID)
			}
			if len(ids) > 0 {
				return &providers.LLMResponse{
					ToolCalls: []providers.ToolCall{
						{ID: "expand", Name: "short_expand", Arguments: map[string]any{"ids": ids}},
					},
				}, nil
			}
		} else {
			p.expandOutput = last.Content
		}
	}
	return &providers.LLMResponse{Content: "answer"}, nil
}

func newSeahorseScenarioLoop(t *testing.T, p providers.LLMProvider) (*AgentLoop, *config.Config) {
	t.Helper()
	base, cfg, mb, _, cleanup := newTestAgentLoop(t)
	base.Close()
	mb.Close()
	cleanup()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "seahorse"
	cfg.Agents.Defaults.ContextWindow = 128000
	cfg.Agents.Defaults.MaxTokens = 256
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	t.Cleanup(al.Close)
	return al, cfg
}

func TestSeahorseScopedRetrievalPreservesMixedSourcesAcrossReopen(t *testing.T) {
	p := &sourceScenarioProvider{tool: "short_grep", args: map[string]any{"pattern": "%probe%", "scope": "message"}}
	al, cfg := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	original := []providers.Message{
		{
			Role:      "user",
			Content:   "probe question",
			SourceID:  "telegram:original",
			CreatedAt: time.Date(2026, 4, 2, 13, 4, 5, 123, time.UTC),
		},
		{
			Role:             "assistant",
			Content:          "probe text beside tool",
			ContentPhase:     providers.ContentPhaseCommentary,
			SourceID:         "answer:original",
			ModelName:        "source-model",
			ReasoningContent: "retained reasoning",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Name: "read_file",
					Function: &providers.FunctionCall{
						Name:             "read_file",
						Arguments:        `{"path":"evidence.txt"}`,
						ThoughtSignature: "opaque-signature",
					},
				},
			},
		},
		{
			Role:       "tool",
			Content:    "probe " + strings.Repeat("complete-tool-result ", 500),
			ToolCallID: "call_1",
			SourceID:   "tool:original",
		},
		{
			Role:     "user",
			Content:  "probe media caption",
			Media:    []string{"media://image-reference"},
			SourceID: "media:original",
		},
	}
	for _, msg := range original {
		agent.Sessions.AddFullMessage(key, msg)
	}
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "first", "query sources")); err != nil {
		t.Fatal(err)
	}
	assertExpandedSources(t, p.expandOutput, original)
	oldID := firstSourceID(t, p.grepOutput)
	al.Close()
	p = &sourceScenarioProvider{tool: "short_grep", args: map[string]any{"pattern": "%probe%", "scope": "message"}}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Close()
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "second", "query sources")); err != nil {
		t.Fatal(err)
	}
	if firstSourceID(t, p.grepOutput) != oldID {
		t.Fatal("reopen changed durable source identity")
	}
	assertExpandedSources(t, p.expandOutput, original)
	p.tool = "short_expand"
	p.args = map[string]any{"ids": []string{oldID}}
	p.expandOutput = ""
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("99", "foreign", "query sources")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.expandOutput, "outside the captured conversation") {
		t.Fatalf("foreign source accepted: %s", p.expandOutput)
	}

	// Changing the prefix cannot turn a foreign stored ID into an owned source.
	p.tool = "short_grep"
	p.args = map[string]any{"pattern": "%query sources%", "scope": "message"}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("99", "own", "query sources")); err != nil {
		t.Fatal(err)
	}
	ownID := firstSourceID(t, p.grepOutput)
	forgedID := ownID[:strings.LastIndex(ownID, "/")+1] + oldID[strings.LastIndex(oldID, "/")+1:]
	p.tool = "short_expand"
	p.args = map[string]any{"ids": []string{forgedID}}
	p.expandOutput = ""
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("99", "forged", "query sources")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.expandOutput, "belongs to another conversation") {
		t.Fatalf("foreign stored ID accepted: %s", p.expandOutput)
	}
	p.tool = "short_grep"
	p.args = map[string]any{"pattern": "%probe%", "all_conversations": true}
	p.expandOutput = ""
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "bypass", "query sources")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.expandOutput, "unsupported scoped retrieval parameter") {
		t.Fatalf("bypass accepted: %s", p.expandOutput)
	}
	reset := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Sender:   bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42"},
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "/clear_inline",
	}
	if response, err := al.processMessage(t.Context(), reset); err != nil || !strings.Contains(response, "reset") {
		t.Fatalf("reset = %q %v", response, err)
	}
	p.tool = "short_expand"
	p.args = map[string]any{"ids": []string{oldID}}
	p.expandOutput = ""
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "reset", "query sources")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.expandOutput, "outside the captured conversation") {
		t.Fatalf("old generation source accepted: %s", p.expandOutput)
	}
}

func firstSourceID(t *testing.T, data string) string {
	t.Helper()
	var found struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(data), &found); err != nil {
		t.Fatal(err)
	}
	if len(found.Messages) == 0 {
		t.Fatal("no source matches")
	}
	return found.Messages[0].ID
}

func assertExpandedSources(t *testing.T, data string, original []providers.Message) {
	t.Helper()
	var result struct {
		Messages []providers.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("expand: %v: %s", err, data)
	}
	for _, want := range original {
		found := false
		for _, got := range result.Messages {
			if got.SourceID == want.SourceID {
				found = true
				actual, _ := json.Marshal(got)
				expected, _ := json.Marshal(want)
				if string(actual) != string(expected) {
					t.Errorf("source %s changed: got %+v", want.SourceID, got)
				}
			}
		}
		if !found {
			t.Errorf("source %s missing", want.SourceID)
		}
	}
}

func TestSeahorseSummaryPromptLabelsUnknownTimestampsAndIncludesMixedParts(t *testing.T) {
	p := &sourceScenarioProvider{}
	al, _ := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.ContextWindow = 9000
	key := inlineConversationKey("main", "42")
	for i := 0; i < 12; i++ {
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "user", Content: strings.Repeat("source evidence ", 1000)},
		)
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{
				Role:    "assistant",
				Content: "text beside tool",
				ToolCalls: []providers.ToolCall{
					{
						ID:   fmt.Sprint(i),
						Name: "evidence_call",
						Function: &providers.FunctionCall{
							Name:      "evidence_call",
							Arguments: `{"path":"mixed-evidence"}`,
						},
					},
				},
			},
		)
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "tool", ToolCallID: fmt.Sprint(i), Content: "full evidence result"},
		)
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "compressed", "next")); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.summaryPrompts) == 0 {
		t.Fatal("compaction did not run")
	}
	prompt := strings.Join(p.summaryPrompts, "\n")
	if strings.Contains(prompt, "0001-01-01") || !strings.Contains(prompt, "unknown time") {
		t.Fatal("unknown provenance rendered as a historical date")
	}
	if !strings.Contains(prompt, "evidence_call") || !strings.Contains(prompt, "mixed-evidence") ||
		!strings.Contains(prompt, "text beside tool") {
		t.Fatal("mixed text/tool parts missing from summary input")
	}
}

type blockedCompactionProvider struct {
	sourceScenarioProvider
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (p *blockedCompactionProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	if len(messages) == 1 && messages[0].Role == "user" {
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
		close(p.canceled)
		<-p.release
		return &providers.LLMResponse{Content: "OLD_LATE_SUMMARY"}, nil
	}
	return p.sourceScenarioProvider.Chat(ctx, messages, defs, model, opts)
}

func TestSeahorseResetCancelsCompactionWithoutWaitingForProviderAndFencesLateSummary(t *testing.T) {
	p := &blockedCompactionProvider{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	al, cfg := newSeahorseScenarioLoop(t, p)
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Sender:   bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42"},
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "OLD_DIALOG",
	}
	if _, err := al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatal(err)
	}
	agent := al.GetRegistry().GetDefaultAgent()
	agent.ContextWindow = 9000
	for i := 0; i < 20; i++ {
		agent.Sessions.AddFullMessage(
			route.SessionKey,
			providers.Message{Role: "user", Content: strings.Repeat("OLD_SOURCE ", 500)},
		)
		agent.Sessions.AddFullMessage(route.SessionKey, providers.Message{Role: "assistant", Content: "old answer"})
	}
	done := make(chan error, 1)
	go func() {
		next := msg
		next.Content = "trigger compression"
		_, e := al.processMessage(t.Context(), next)
		done <- e
	}()
	awaitSignal(t, p.started, "compaction start")
	reset := msg
	reset.Content = "/clear"
	response, err := al.processMessage(t.Context(), reset)
	if err != nil || !strings.Contains(response, "cleared") {
		t.Fatalf("reset = %q %v", response, err)
	}
	awaitSignal(t, p.canceled, "compaction cancellation")
	close(p.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old turn did not stop")
	}
	al.Close()
	fresh := &sourceScenarioProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), fresh)
	defer al.Close()
	msg.Content = "new conversation"
	if _, err = al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(fresh.lastInput)
	for _, old := range []string{"OLD_DIALOG", "OLD_SOURCE", "OLD_LATE_SUMMARY"} {
		if strings.Contains(input, old) {
			t.Errorf("reset leaked %s", old)
		}
	}
}

func TestSeahorseCloseWaitsForCanceledCompactionBeforeStorageClose(t *testing.T) {
	p := &blockedCompactionProvider{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	al, _ := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.ContextWindow = 9000
	key := inlineConversationKey("main", "42")
	for i := 0; i < 20; i++ {
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: strings.Repeat("source ", 1000)})
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "assistant", Content: "answer"})
	}
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = al.processMessage(t.Context(), selectedInlineMessage("42", "close", "next"))
	}()
	awaitSignal(t, p.started, "compaction start")
	closed := make(chan struct{})
	go func() { al.Close(); close(closed) }()
	awaitSignal(t, p.canceled, "close cancellation")
	select {
	case <-closed:
		t.Fatal("storage closed while provider worker was still running")
	default:
	}
	close(p.release)
	awaitSignal(t, closed, "manager close")
	awaitSignal(t, turnDone, "turn completion")
}

func awaitSignal(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func TestSeahorseReloadCancelsAndWaitsForOldWorkers(t *testing.T) {
	p := &blockedCompactionProvider{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	al, cfg := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.ContextWindow = 9000
	key := inlineConversationKey("main", "42")
	for i := 0; i < 20; i++ {
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "user", Content: strings.Repeat("reload source ", 1000)},
		)
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "assistant", Content: "answer"})
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var release sync.Once
	defer release.Do(func() { close(p.release) })
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = al.processMessage(runCtx, selectedInlineMessage("42", "reload", "next"))
	}()
	awaitSignal(t, p.started, "compaction start")
	reloaded := make(chan error, 1)
	go func() { reloaded <- al.ReloadProviderAndConfig(t.Context(), &sourceScenarioProvider{}, cfg) }()
	select {
	case <-p.canceled:
	case err := <-reloaded:
		cancel()
		t.Fatalf("reload finished before canceling old workers: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("reload did not cancel old worker")
	}
	select {
	case err := <-reloaded:
		t.Fatalf("reload finished while old worker was still running: %v", err)
	default:
	}
	release.Do(func() { close(p.release) })
	select {
	case err := <-reloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not finish")
	}
	awaitSignal(t, turnDone, "old turn completion")
}

func TestSeahorseCompactionUsesOwningAgentsExactModelConfigurationAndOptions(t *testing.T) {
	var mu sync.Mutex
	var compactKeys, answerKeys []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model       string              `json:"model"`
			Messages    []providers.Message `json:"messages"`
			Temperature float64             `json:"temperature"`
			MaxTokens   int                 `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		key := r.Header.Get("Authorization")
		mu.Lock()
		defer mu.Unlock()
		if len(request.Messages) == 1 && request.Messages[0].Role == "user" {
			compactKeys = append(compactKeys, key)
			if request.Model != "shared" || request.Temperature != 0.23 || request.MaxTokens != 123 {
				t.Errorf("compaction ignored owning options: %+v", request)
			}
		} else {
			answerKeys = append(answerKeys, key)
		}
		_, _ = w.Write(
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"COMPACT_SUMMARY"},"finish_reason":"stop"}]}`),
		)
	}))
	defer endpoint.Close()
	initial, cfg := newSeahorseScenarioLoop(t, &sourceScenarioProvider{})
	initial.Close()
	temperature := 0.23
	cfg.Agents.Defaults.Temperature = &temperature
	cfg.Agents.Defaults.MaxTokens = 123
	cfg.Agents.Defaults.ContextWindow = 12000
	cfg.Agents.Defaults.SummarizeTokenPercent = 100
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "test-model", Model: "openai/main", APIBase: endpoint.URL, APIKey: "main-key"},
		{ModelName: "reviewer-model", Model: "openai/shared", APIBase: endpoint.URL, APIKey: "reviewer-A"},
		{ModelName: "reviewer-model", Model: "openai/shared", APIBase: endpoint.URL, APIKey: "reviewer-B"},
	}
	reviewerWorkspace := t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "reviewer", Workspace: reviewerWorkspace, Model: &config.AgentModelConfig{Primary: "reviewer-model"}},
	}
	cfg.Bindings = []config.AgentBinding{
		{
			AgentID: "reviewer",
			Match:   config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "42"}},
		},
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "42",
		SenderID: "telegram:42",
		Peer:     bus.Peer{Kind: "direct", ID: "42"},
		Content:  "next",
	}
	route, reviewer, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		reviewer.Sessions.AddFullMessage(
			route.SessionKey,
			providers.Message{Role: "user", Content: strings.Repeat("evidence ", 1500)},
		)
		reviewer.Sessions.AddFullMessage(route.SessionKey, providers.Message{Role: "assistant", Content: "answer"})
	}
	if _, err = al.processMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(compactKeys) == 0 || len(answerKeys) == 0 {
		t.Fatalf("calls: compaction=%v answer=%v", compactKeys, answerKeys)
	}
	history := reviewer.Sessions.GetHistory(route.SessionKey)
	if history[len(history)-1].ModelName != "shared" {
		t.Fatalf("new answer lost model provenance: %q", history[len(history)-1].ModelName)
	}
	for _, key := range compactKeys {
		if key != answerKeys[0] || key == "Bearer main-key" {
			t.Errorf("agent selection diverged: compact=%s answer=%s", key, answerKeys[0])
		}
	}
	if _, err = os.Stat(filepath.Join(reviewerWorkspace, "sessions", "seahorse-reviewer.sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(
		filepath.Join(cfg.WorkspacePath(), "sessions", "seahorse-reviewer.sqlite"),
	); !os.IsNotExist(
		err,
	) {
		t.Fatal("reviewer context stored in default agent workspace")
	}
}

func TestSeahorseBoundsCompletionAttemptsAndRetainsSourcesOnLimit(t *testing.T) {
	p := &sourceScenarioProvider{}
	initial, cfg := newSeahorseScenarioLoop(t, p)
	initial.Close()
	cfg.Agents.Defaults.ContextWindow = 9000
	cfg.Agents.Defaults.ContextManagerConfig.MaxCompletionAttempts = 1
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	for i := 0; i < 16; i++ {
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "user", Content: strings.Repeat("bounded source ", 1500)},
		)
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "assistant", Content: "answer"})
	}
	_, err := al.processMessage(t.Context(), selectedInlineMessage("42", "bounded", "next"))
	if err == nil || !strings.Contains(err.Error(), "completion attempt limit") {
		t.Fatalf("expected visible attempt limit, got %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.summaryPrompts) != 1 {
		t.Fatalf("provider attempts = %d, want 1", len(p.summaryPrompts))
	}
	if len(agent.Sessions.GetHistory(key)) != 32 {
		t.Fatal("attempt limit truncated canonical evidence")
	}
}

type concurrencyCompactionProvider struct {
	active atomic.Int32
	peak   atomic.Int32
	calls  atomic.Int32
}

func (p *concurrencyCompactionProvider) GetDefaultModel() string { return "test-model" }

func (p *concurrencyCompactionProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if len(messages) == 1 && messages[0].Role == "user" {
		p.calls.Add(1)
		active := p.active.Add(1)
		defer p.active.Add(-1)
		for {
			old := p.peak.Load()
			if active <= old || p.peak.CompareAndSwap(old, active) {
				break
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "answer"}, nil
}

func TestSeahorseBoundsConcurrentCompactionsAndCancelsQueuedWork(t *testing.T) {
	workspace := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		p := &concurrencyCompactionProvider{}
		agent := &AgentInstance{
			ID:              "main",
			Workspace:       workspace,
			Provider:        p,
			ContextProvider: p,
			Model:           "test-model",
			MaxTokens:       256,
		}
		manager, err := NewContextManager(agent, "seahorse", config.SeahorseContextConfig{MaxConcurrentCompactions: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var done sync.WaitGroup
		for _, key := range []string{"conversation:one", "conversation:two"} {
			snapshot := ContextSnapshot{
				SessionKey: key,
				ScopeKey:   key,
				Provider:   p,
				Model:      "test-model",
				Options:    map[string]any{"max_tokens": 256, "temperature": 0.7},
			}
			for i := 0; i < 8; i++ {
				snapshot.History = append(
					snapshot.History,
					providers.Message{Role: "user", Content: strings.Repeat("concurrent source ", 1500)},
					providers.Message{Role: "assistant", Content: "answer"},
				)
			}
			done.Add(1)
			go func() { defer done.Done(); _ = manager.Compact(ctx, snapshot, 5000) }()
		}
		synctest.Wait()
		if p.calls.Load() != 1 || p.peak.Load() != 1 {
			t.Errorf("compaction concurrency ignored: calls=%d peak=%d", p.calls.Load(), p.peak.Load())
		}
		cancel()
		done.Wait()
		if p.calls.Load() != 1 {
			t.Error("canceled queued compaction called provider")
		}
	})
}

func TestSeahorseRecordsSuccessfulFallbackModelInCanonicalSources(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model == "primary" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
			return
		}
		_, _ = w.Write(
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"backup answer"},"finish_reason":"stop"}]}`),
		)
	}))
	defer endpoint.Close()
	initial, cfg := newSeahorseScenarioLoop(t, &sourceScenarioProvider{})
	initial.Close()
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "test-model", Model: "openai/primary", APIBase: endpoint.URL, APIKey: "primary-key"},
		{ModelName: "backup", Model: "openai/backup", APIBase: endpoint.URL, APIKey: "backup-key"},
	}
	cfg.Agents.Defaults.ModelFallbacks = []string{"backup"}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	if _, err = al.processMessage(t.Context(), selectedInlineMessage("42", "fallback", "answer")); err != nil {
		t.Fatal(err)
	}
	history := al.GetRegistry().GetDefaultAgent().Sessions.GetHistory(inlineConversationKey("main", "42"))
	if history[len(history)-1].ModelName != "backup" {
		t.Fatalf("fallback source reports model %q", history[len(history)-1].ModelName)
	}
}
