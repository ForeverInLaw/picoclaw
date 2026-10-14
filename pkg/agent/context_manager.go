package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tokenizer"
)

// ContextManager owns active-context storage; canonical transcripts, archived
// chat memory and Saved facts retain their separate persistence boundaries.
type ContextManager interface {
	Name() string
	Assemble(ctx context.Context, snapshot ContextSnapshot) (ContextAssembly, error)
	Ingest(ctx context.Context, snapshot ContextSnapshot) error
	Compact(ctx context.Context, snapshot ContextSnapshot, budget int) error
	Clear(ctx context.Context, sessionKey string) error
	Close() error
}

type ContextSnapshot struct {
	SessionKey string
	ScopeKey   string
	Guard      session.ConversationTurn
	History    []providers.Message
	Summary    string
	Provider   providers.LLMProvider
	Model      string
	Options    map[string]any
}

type ContextAssembly struct {
	History []providers.Message
	Summary string
}

func snapshotContext(agent *AgentInstance, key string) ContextSnapshot {
	snapshot := ContextSnapshot{
		SessionKey: key,
		ScopeKey:   key,
		History:    agent.Sessions.GetHistory(key),
		Summary:    agent.Sessions.GetSummary(key),
		Provider:   agent.ContextProvider,
		Model:      resolvedCandidateModel(agent.Candidates, agent.Model),
		Options: map[string]any{
			"temperature":      agent.Temperature,
			"max_tokens":       agent.MaxTokens,
			"prompt_cache_key": "seahorse:" + agent.ID,
		},
	}
	if snapshot.Provider == nil {
		snapshot.Provider = agent.Provider
	}
	if tc, ok := snapshot.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() &&
		agent.ThinkingLevel != ThinkingOff {
		snapshot.Options["thinking_level"] = string(agent.ThinkingLevel)
	}
	if guard, ok := agent.Sessions.(session.ConversationTurn); ok {
		snapshot.Guard = guard
		snapshot.ScopeKey = guard.ConversationKey(key)
	}
	snapshot.ScopeKey = agent.ID + "\x00" + snapshot.ScopeKey
	return snapshot
}

type legacyContextManager struct{}

func (legacyContextManager) Name() string { return "legacy" }
func (legacyContextManager) Assemble(_ context.Context, s ContextSnapshot) (ContextAssembly, error) {
	return ContextAssembly{History: s.History, Summary: s.Summary}, nil
}

func (legacyContextManager) Ingest(ctx context.Context, snapshot ContextSnapshot) error { return nil }

func (legacyContextManager) Compact(ctx context.Context, snapshot ContextSnapshot, budget int) error {
	return nil
}
func (legacyContextManager) Clear(ctx context.Context, sessionKey string) error { return nil }
func (legacyContextManager) Close() error                                       { return nil }

type seahorseContextManager struct {
	engine            *seahorse.Engine
	options           config.SeahorseContextConfig
	lifetime          context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	closing           bool
	retainedProviders []providers.StatefulProvider
	closed            chan struct{}
	closeErr          error
	nextOperation     uint64
	operations        map[uint64]contextOperation
	workers           sync.WaitGroup
	compactionSlots   chan struct{}
	conversationSlots [256]chan struct{}
	background        sync.Map
}
type contextOperation struct {
	key    string
	cancel context.CancelFunc
}
type (
	completionBudgetKey struct{}
	contextRuntimeKey   struct{}
	completionBudget    struct{ used int }
)

// NewContextManager opens the selected active-context engine for one agent.
func NewContextManager(
	agent *AgentInstance,
	name string,
	options config.SeahorseContextConfig,
) (ContextManager, error) {
	if name == "" || name == "legacy" {
		return legacyContextManager{}, nil
	}
	if name != "seahorse" {
		return nil, fmt.Errorf("unknown context_manager %q", name)
	}
	if options.CompactTimeoutSeconds <= 0 {
		options.CompactTimeoutSeconds = 60
	}
	if options.MaxCompactIterations <= 0 {
		options.MaxCompactIterations = 20
	}
	if options.MaxCompletionAttempts <= 0 {
		options.MaxCompletionAttempts = 4
	}
	if options.MaxConcurrentCompactions <= 0 {
		options.MaxConcurrentCompactions = 2
	}
	lifetime, cancel := context.WithCancel(context.Background())
	m := &seahorseContextManager{
		options:         options,
		lifetime:        lifetime,
		cancel:          cancel,
		closed:          make(chan struct{}),
		operations:      map[uint64]contextOperation{},
		compactionSlots: make(chan struct{}, options.MaxConcurrentCompactions),
	}
	for i := range m.conversationSlots {
		m.conversationSlots[i] = make(chan struct{}, 1)
	}
	engine, err := seahorse.NewEngine(
		seahorse.Config{
			DBPath:               filepath.Join(agent.Workspace, "sessions", "seahorse-"+agent.ID+".sqlite"),
			MaxCompactIterations: options.MaxCompactIterations,
		},
		m.complete,
	)
	if err != nil {
		cancel()
		return nil, err
	}
	m.engine = engine
	return m, nil
}

func (*seahorseContextManager) Name() string { return "seahorse" }

func (m *seahorseContextManager) begin(ctx context.Context, s ContextSnapshot) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, nil, fmt.Errorf("context manager is closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, contextRuntimeKey{}, s)
	ctx = seahorse.WithCompletionModel(ctx, s.Model)
	stop := context.AfterFunc(m.lifetime, cancel)
	if s.Guard != nil {
		ctx = seahorse.WithCommitGuard(ctx, s.Guard.CommitIfCurrent)
	}
	m.nextOperation++
	id := m.nextOperation
	m.operations[id] = contextOperation{key: s.SessionKey, cancel: cancel}
	m.workers.Add(1)
	done := func() {
		stop()
		cancel()
		m.mu.Lock()
		delete(m.operations, id)
		m.mu.Unlock()
		m.workers.Done()
	}
	return ctx, done, nil
}

func (m *seahorseContextManager) Ingest(ctx context.Context, s ContextSnapshot) error {
	ctx, done, err := m.begin(ctx, s)
	if err != nil {
		return err
	}
	defer done()
	messages := make([]seahorse.Message, len(s.History))
	for i, msg := range s.History {
		messages[i] = toSeahorseMessage(msg, s.ScopeKey, i)
	}
	return m.engine.SyncSources(ctx, s.ScopeKey, messages)
}

func (m *seahorseContextManager) Assemble(ctx context.Context, s ContextSnapshot) (ContextAssembly, error) {
	if err := m.Ingest(ctx, s); err != nil {
		return ContextAssembly{}, err
	}
	ctx, done, err := m.begin(ctx, s)
	if err != nil {
		return ContextAssembly{}, err
	}
	defer done()
	// Retain the complete active graph until the caller accounts for system,
	// tools, media and output reserve and requests source-linked compaction.
	assembled, err := m.engine.Assemble(ctx, s.ScopeKey, seahorse.AssembleInput{Budget: int(^uint(0) >> 1)})
	if err != nil {
		return ContextAssembly{}, err
	}
	result := ContextAssembly{Summary: s.Summary}
	if assembled != nil {
		for _, msg := range assembled.Messages {
			result.History = append(result.History, fromSeahorseMessage(msg))
		}
		if assembled.Summary != "" {
			activeSummary := scopedContextReferences(assembled.Summary, s.ScopeKey)
			if result.Summary != "" {
				result.Summary += "\n\n"
			}
			result.Summary += activeSummary
		}
	}
	if s.Guard != nil {
		current, guardErr := s.Guard.IsCurrent()
		if guardErr != nil {
			return ContextAssembly{}, guardErr
		}
		if !current {
			return ContextAssembly{}, context.Canceled
		}
	}
	return result, nil
}

func (m *seahorseContextManager) Compact(ctx context.Context, s ContextSnapshot, budget int) error {
	ctx, done, err := m.begin(ctx, s)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.options.CompactTimeoutSeconds)*time.Second)
	defer cancel()
	if err = m.Ingest(ctx, s); err != nil {
		return err
	}
	select {
	case m.compactionSlots <- struct{}{}:
		defer func() { <-m.compactionSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	scopeHash := sha256.Sum256([]byte(s.ScopeKey))
	semaphore := m.conversationSlots[scopeHash[0]]
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	ctx = context.WithValue(ctx, completionBudgetKey{}, &completionBudget{})
	_, err = m.engine.CompactUntilUnder(ctx, s.ScopeKey, max(1, budget))
	return err
}

func (m *seahorseContextManager) complete(
	ctx context.Context,
	prompt string,
	options seahorse.CompleteOptions,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if budget, ok := ctx.Value(completionBudgetKey{}).(*completionBudget); ok {
		if budget.used >= m.options.MaxCompletionAttempts {
			return "", fmt.Errorf("compaction completion attempt limit reached")
		}
		budget.used++
	}
	runtime, ok := ctx.Value(contextRuntimeKey{}).(ContextSnapshot)
	if !ok || runtime.Provider == nil {
		return "", fmt.Errorf("compaction has no captured agent provider")
	}
	requestOptions := make(map[string]any, len(runtime.Options))
	for key, value := range runtime.Options {
		requestOptions[key] = value
	}
	maxTokens, ok := runtime.Options["max_tokens"].(int)
	if !ok || maxTokens <= 0 {
		return "", fmt.Errorf("compaction has no configured output limit")
	}
	requestOptions["max_tokens"] = min(options.MaxTokens, maxTokens)
	response, err := runtime.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt}},
		nil,
		runtime.Model,
		requestOptions,
	)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", fmt.Errorf("compaction provider returned no response")
	}
	return response.Content, nil
}

// Clear cancels operations belonging to the old conversation. Reset advances
// its generation in ConversationStore; source graphs remain archived on disk.
func (m *seahorseContextManager) Clear(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, operation := range m.operations {
		if operation.key == key {
			operation.cancel()
		}
	}
	return nil
}

func (m *seahorseContextManager) Close() error {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		<-m.closed
		return m.closeErr
	}
	m.closing = true
	m.cancel()
	m.mu.Unlock()
	m.workers.Wait()
	m.closeErr = m.engine.Close()
	for _, provider := range m.retainedProviders {
		provider.Close()
	}
	close(m.closed)
	return m.closeErr
}

func toSeahorseMessage(msg providers.Message, scope string, index int) seahorse.Message {
	if msg.SourceID == "" {
		msg.SourceID = fmt.Sprintf("%x:%d", sha256.Sum256([]byte(scope)), index)
	}
	msg.ToolCalls = append([]providers.ToolCall(nil), msg.ToolCalls...)
	for i := range msg.ToolCalls {
		call := &msg.ToolCalls[i]
		if call.Function == nil {
			args, _ := json.Marshal(call.Arguments)
			call.Function = &providers.FunctionCall{Name: call.Name, Arguments: string(args)}
		}
	}
	payload, _ := json.Marshal(msg)
	result := seahorse.Message{
		Role:             msg.Role,
		Content:          msg.Content,
		ModelName:        msg.ModelName,
		ReasoningContent: msg.ReasoningContent,
		CreatedAt:        msg.CreatedAt,
		TokenCount:       tokenizer.EstimateMessageTokens(msg),
		Payload:          payload,
	}
	if msg.Content != "" {
		result.Parts = append(result.Parts, seahorse.MessagePart{Type: "text", Text: msg.Content})
	}
	for _, call := range msg.ToolCalls {
		result.Parts = append(
			result.Parts,
			seahorse.MessagePart{
				Type:       "tool_use",
				Name:       call.Function.Name,
				Arguments:  call.Function.Arguments,
				ToolCallID: call.ID,
			},
		)
	}
	if msg.ToolCallID != "" {
		result.Parts = append(
			result.Parts,
			seahorse.MessagePart{Type: "tool_result", ToolCallID: msg.ToolCallID, Text: msg.Content},
		)
	}
	for _, uri := range msg.Media {
		result.Parts = append(result.Parts, seahorse.MessagePart{Type: "media", MediaURI: uri})
	}
	return result
}

func fromSeahorseMessage(msg seahorse.Message) providers.Message {
	var result providers.Message
	if len(msg.Payload) > 0 && json.Unmarshal(msg.Payload, &result) == nil {
		return result
	}
	result = providers.Message{
		Role:             msg.Role,
		Content:          msg.Content,
		ModelName:        msg.ModelName,
		ReasoningContent: msg.ReasoningContent,
		CreatedAt:        msg.CreatedAt,
	}
	for _, part := range msg.Parts {
		switch part.Type {
		case "tool_use":
			result.ToolCalls = append(
				result.ToolCalls,
				providers.ToolCall{
					ID:       part.ToolCallID,
					Type:     "function",
					Function: &providers.FunctionCall{Name: part.Name, Arguments: part.Arguments},
				},
			)
		case "tool_result":
			result.ToolCallID = part.ToolCallID
		case "media":
			result.Media = append(result.Media, part.MediaURI)
		}
	}
	return result
}

func contextManagerSettings(
	agent *config.AgentConfig,
	defaults *config.AgentDefaults,
) (string, config.SeahorseContextConfig) {
	name, options := strings.TrimSpace(defaults.ContextManager), defaults.ContextManagerConfig
	if agent != nil {
		if agent.ContextManager != "" {
			name = agent.ContextManager
		}
		if agent.ContextManagerConfig != nil {
			options = *agent.ContextManagerConfig
		}
	}
	return name, options
}

var contextReferencePattern = regexp.MustCompile(`msg:[0-9]+|sum_[a-f0-9]+`)

func contextReferenceScope(scope string) string {
	return fmt.Sprintf("scope:%x/", sha256.Sum256([]byte(scope)))
}

func scopedContextReferences(text, scope string) string {
	return contextReferencePattern.ReplaceAllStringFunc(
		text,
		func(id string) string { return contextReferenceScope(scope) + id },
	)
}

func contextHistory(ctx context.Context, agent *AgentInstance, key string, protected int) (ContextAssembly, error) {
	s := snapshotContext(agent, key)
	protected = min(protected, len(s.History))
	tail := append([]providers.Message(nil), s.History[len(s.History)-protected:]...)
	s.History = s.History[:len(s.History)-protected]
	if agent.ContextManagerError != nil {
		return ContextAssembly{}, agent.ContextManagerError
	}
	if agent.ContextManager == nil {
		return ContextAssembly{History: append(s.History, tail...), Summary: s.Summary}, nil
	}
	result, err := agent.ContextManager.Assemble(ctx, s)
	result.History = append(result.History, tail...)
	return result, err
}

func (al *AgentLoop) compressContext(
	ctx context.Context,
	agent *AgentInstance,
	key string,
	protected int,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	reserve int,
) (compressionResult, bool, error) {
	if agent.ContextManager == nil || agent.ContextManager.Name() == "legacy" {
		result, ok := al.forceCompression(agent, key, protected)
		return result, ok, nil
	}
	s := snapshotContext(agent, key)
	s.History = s.History[:max(0, len(s.History)-protected)]
	before, err := agent.ContextManager.Assemble(ctx, s)
	if err != nil {
		return compressionResult{}, false, err
	}
	historyTokens := 0
	for _, msg := range before.History {
		historyTokens += estimateMessageTokens(msg)
	}
	contributors := estimateRequestTokens(messages, defs, reserve) - historyTokens
	// Leave room for source references and the summary's XML envelope.
	budget := max(1, min(historyTokens*3/4, agent.ContextWindow-contributors-512))
	if err = agent.ContextManager.Compact(ctx, s, budget); err != nil {
		return compressionResult{}, false, err
	}
	after, err := agent.ContextManager.Assemble(ctx, s)
	if err != nil {
		return compressionResult{}, false, err
	}
	dropped := len(before.History) - len(after.History)
	return compressionResult{
		DroppedMessages:   dropped,
		RemainingMessages: len(after.History),
	}, dropped > 0 ||
		before.Summary != after.Summary, nil
}

func estimateTokens(messages []providers.Message) int {
	n := 0
	for _, message := range messages {
		n += estimateMessageTokens(message)
	}
	return n
}

func (m *seahorseContextManager) scheduleCompact(ctx context.Context, s ContextSnapshot, budget int) {
	if _, loaded := m.background.LoadOrStore(s.ScopeKey, true); loaded {
		return
	}
	tracked, done, err := m.begin(ctx, s)
	if err != nil {
		m.background.Delete(s.ScopeKey)
		return
	}
	go func() {
		defer done()
		defer m.background.Delete(s.ScopeKey)
		if err := m.Compact(tracked, s, budget); err != nil && !errors.Is(err, context.Canceled) {
			logger.WarnCF(
				"agent",
				"Context compaction failed",
				map[string]any{"session_key": s.SessionKey, "error": err.Error()},
			)
		}
	}()
}

// Providers captured by earlier compactions remain usable until every worker
// has joined, including when a command switches the agent's answer model.
func (m *seahorseContextManager) retainProvider(provider providers.LLMProvider) {
	stateful, ok := provider.(providers.StatefulProvider)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.retainedProviders {
		if existing == stateful {
			return
		}
	}
	if m.closing {
		m.mu.Unlock()
		<-m.closed
		stateful.Close()
		m.mu.Lock()
		return
	}
	m.retainedProviders = append(m.retainedProviders, stateful)
}
