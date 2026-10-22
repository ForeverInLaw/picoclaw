// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/factcheck"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/voice"
)

type AgentLoop struct {
	// Core dependencies
	bus      *bus.MessageBus
	cfg      *config.Config
	registry *AgentRegistry
	state    *state.Manager

	// Event system (from Incoming)
	eventBus *EventBus
	hooks    *HookManager

	// Runtime state
	running        atomic.Bool
	fallback       *providers.FallbackChain
	channelManager *channels.Manager
	mediaStore     media.MediaStore
	transcriber    voice.Transcriber
	cmdRegistry    *commands.Registry
	mcp            mcpRuntime
	hookRuntime    hookRuntime
	steering       *steeringQueue
	mu             sync.RWMutex

	// Concurrent turn management (from HEAD)
	activeTurnStates sync.Map     // key: sessionKey (string), value: *turnState
	subTurnCounter   atomic.Int64 // Counter for generating unique SubTurn IDs

	// Turn tracking (from Incoming)
	turnSeq        atomic.Uint64
	activeRequests sync.WaitGroup

	runtimeStatus   *modelruntime.Collector
	temporaryModels map[string]temporaryModelSelection
	routingSource   func() (*config.Config, error)
	reloadFunc      func() error
	runCancel       context.CancelFunc
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey              string              // Session identifier for history/context
	Channel                 string              // Target channel for tool execution
	ChatID                  string              // Target chat ID for tool execution
	PeerKind                string              // Peer kind for memory/chat routing
	ChatLabel               string              // Human-readable chat label
	Language                string              // Detected language hint for user-facing tool responses
	ResponseQuote           string              // Optional quote block source shown above assistant output
	Sender                  bus.SenderInfo      // Structured sender info for tools
	SenderID                string              // Current sender ID for dynamic context
	SenderDisplayName       string              // Current sender display name for dynamic context
	TelegramUserID          string              // Stable user identity, independent of inline transport
	ReplyToMessageID        string              // Original inbound message ID for threaded replies
	UserMessage             string              // User message content (may include prefix)
	SystemPromptOverride    string              // Override the default system prompt (Used by SubTurns)
	Media                   []string            // media:// refs from inbound message
	InitialSteeringMessages []providers.Message // Steering messages from refactor/agent
	DefaultResponse         string              // Response when LLM returns empty
	EnableSummary           bool                // Whether to trigger summarization
	SendResponse            bool                // Whether to send response via bus
	NoHistory               bool                // If true, don't load session history (for heartbeat)
	AllowedTools            []string            // Optional per-turn tool allowlist
	DisableToolFeedback     bool                // Suppress raw tool feedback for this turn
	SkipInitialSteeringPoll bool                // If true, skip the steering poll at loop start (used by Continue)
	ConversationContext     ConversationContext // Automatic memory boundary for this conversation
}

type continuationTarget struct {
	SessionKey       string
	Channel          string
	ChatID           string
	ReplyToMessageID string
}

const (
	maxPendingRequests        = 64
	defaultResponse           = "The model returned an empty response. This may indicate a provider error or token limit."
	toolLimitResponse         = "I've reached `max_tool_iterations` without a final response. Increase `max_tool_iterations` in config.json if this task needs more tool steps."
	sessionKeyAgentPrefix     = "agent:"
	metadataKeyAccountID      = "account_id"
	metadataKeyGuildID        = "guild_id"
	metadataKeyTeamID         = "team_id"
	metadataKeyParentPeerKind = "parent_peer_kind"
	metadataKeyParentPeerID   = "parent_peer_id"
)

// NewAgentLoop accepts an optional saved-routing source at construction so
// context engines initialize independently of an unavailable primary provider.
func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	routingSources ...func() (*config.Config, error),
) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider, len(routingSources) > 0 && routingSources[0] != nil)

	// Set up shared fallback chain
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	eventBus := NewEventBus()
	al := &AgentLoop{
		bus:         msgBus,
		cfg:         cfg,
		registry:    registry,
		state:       stateManager,
		eventBus:    eventBus,
		fallback:    fallbackChain,
		cmdRegistry: commands.NewRegistry(commands.BuiltinDefinitions()),
		steering:    newSteeringQueue(parseSteeringMode(cfg.Agents.Defaults.SteeringMode)),
	}
	if len(routingSources) > 0 {
		al.routingSource = routingSources[0]
	}
	al.hooks = NewHookManager(eventBus)
	configureHookManagerFromConfig(al.hooks, cfg)

	// Register shared tools to all agents (now that al is created)
	registerSharedTools(al, cfg, msgBus, registry, provider)

	return al
}

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).
func registerSharedTools(
	al *AgentLoop,
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) {
	allowReadPaths := buildAllowReadPatterns(cfg)

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		searchOpts := tools.WebSearchToolOptions{
			PrimaryProvider:      cfg.Tools.Web.PrimaryProvider,
			ParallelAPIKeys:      config.MergeAPIKeys(cfg.Tools.Web.Parallel.APIKey, cfg.Tools.Web.Parallel.APIKeys),
			ParallelBaseURL:      cfg.Tools.Web.Parallel.BaseURL,
			ParallelMaxResults:   cfg.Tools.Web.Parallel.MaxResults,
			ParallelEnabled:      cfg.Tools.Web.Parallel.Enabled,
			ExaAPIKeys:           config.MergeAPIKeys(cfg.Tools.Web.Exa.APIKey, cfg.Tools.Web.Exa.APIKeys),
			ExaBaseURL:           cfg.Tools.Web.Exa.BaseURL,
			ExaMaxResults:        cfg.Tools.Web.Exa.MaxResults,
			ExaEnabled:           cfg.Tools.Web.Exa.Enabled,
			BraveAPIKeys:         config.MergeAPIKeys(cfg.Tools.Web.Brave.APIKey, cfg.Tools.Web.Brave.APIKeys),
			BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
			BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
			TavilyAPIKeys:        config.MergeAPIKeys(cfg.Tools.Web.Tavily.APIKey, cfg.Tools.Web.Tavily.APIKeys),
			TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
			TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
			TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
			DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			PerplexityAPIKeys: config.MergeAPIKeys(
				cfg.Tools.Web.Perplexity.APIKey,
				cfg.Tools.Web.Perplexity.APIKeys,
			),
			PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
			PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
			SearXNGBaseURL:       cfg.Tools.Web.SearXNG.BaseURL,
			SearXNGMaxResults:    cfg.Tools.Web.SearXNG.MaxResults,
			SearXNGEnabled:       cfg.Tools.Web.SearXNG.Enabled,
			GLMSearchAPIKey:      cfg.Tools.Web.GLMSearch.APIKey,
			GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
			GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
			GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
			GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
			Proxy:                cfg.Tools.Web.Proxy,
		}
		searchProvider, searchMaxResults, err := tools.NewWebSearchProvider(searchOpts)
		if err != nil {
			logger.ErrorCF("agent", "Failed to create web search provider", map[string]any{"error": err.Error()})
		} else if cfg.Tools.IsToolEnabled("web") && searchProvider != nil {
			agent.Tools.Register(tools.NewWebSearchToolWithProvider(searchProvider, searchMaxResults))
		}

		var fetchTool *tools.WebFetchTool
		if cfg.Tools.IsToolEnabled("web_fetch") || cfg.Tools.IsToolEnabled("fact_check") {
			fetchTool, err = tools.NewWebFetchToolWithProxy(
				50000,
				cfg.Tools.Web.Proxy,
				cfg.Tools.Web.Format,
				cfg.Tools.Web.FetchLimitBytes,
				cfg.Tools.Web.PrivateHostWhitelist)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else if cfg.Tools.IsToolEnabled("web_fetch") {
				agent.Tools.Register(fetchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("fact_check") {
			if searchProvider == nil || fetchTool == nil {
				logger.ErrorCF("agent", "Failed to create fact check tool", map[string]any{
					"reason": "search provider or fetch tool unavailable",
				})
			} else {
				agent.Tools.Register(tools.NewFactCheckTool(factcheck.NewService(searchProvider, fetchTool)))
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			messageTool.SetSendCallback(func(toolCtx context.Context, channel, chatID, content string) error {
				return publishToolMessage(msgBus, toolCtx, channel, chatID, content)
			})
			agent.Tools.Register(messageTool)
		}
		if cfg.Tools.IsToolEnabled("send_sticker") {
			agent.Tools.Register(tools.NewSendStickerTool(
				cfg.Channels.Telegram.Token,
				cfg.Channels.Telegram.BaseURL,
			))
		}
		if cfg.Tools.IsToolEnabled("personal_todo") {
			if tool, ok := agent.Tools.Get("personal_todo"); ok {
				if todoTool, ok := tool.(*tools.PersonalTodoTool); ok {
					todoTool.SetSendCallback(func(toolCtx context.Context, channel, chatID, content string) error {
						return publishToolMessage(msgBus, toolCtx, channel, chatID, content)
					})
				}
			}
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(sendFileTool)
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
				MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
				ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
			})

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// Spawn and spawn_status tools share a SubagentManager.
		// Construct it when either tool is enabled (both require subagent).
		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		spawnStatusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
		if (spawnEnabled || spawnStatusEnabled) && cfg.Tools.IsToolEnabled("subagent") {
			subagentManager := tools.NewSubagentManager(provider, agent.Model, agent.Workspace)
			subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)

			// Set the spawner that links into AgentLoop's turnState
			subagentManager.SetSpawner(func(
				ctx context.Context,
				task, label, targetAgentID string,
				tls *tools.ToolRegistry,
				maxTokens int,
				temperature float64,
				hasMaxTokens, hasTemperature bool,
			) (*tools.ToolResult, error) {
				// 1. Recover parent Turn State from Context
				parentTS := turnStateFromContext(ctx)
				if parentTS == nil {
					// Fallback: If no turnState exists in context, create an isolated ad-hoc root turn state
					// so that the tool can still function outside of an agent loop (e.g. tests, raw invocations).
					parentTS = &turnState{
						ctx:            ctx,
						turnID:         "adhoc-root",
						depth:          0,
						session:        nil, // Ephemeral session not needed for adhoc spawn
						pendingResults: make(chan *tools.ToolResult, 16),
						concurrencySem: make(chan struct{}, 5),
					}
				}

				// 2. Build Tools slice from registry
				var tlSlice []tools.Tool
				for _, name := range tls.List() {
					if t, ok := tls.Get(name); ok {
						tlSlice = append(tlSlice, t)
					}
				}

				// 3. System Prompt
				systemPrompt := "You are a subagent. Complete the given task independently and report the result.\n" +
					"You have access to tools - use them as needed to complete your task.\n" +
					"After completing the task, provide a clear summary of what was done.\n\n" +
					"Task: " + task

				// 4. Resolve Model
				modelToUse := agent.Model
				if targetAgentID != "" {
					if targetAgent, ok := al.GetRegistry().GetAgent(targetAgentID); ok {
						modelToUse = targetAgent.Model
					}
				}

				// 5. Build SubTurnConfig
				cfg := SubTurnConfig{
					Model:        modelToUse,
					Tools:        tlSlice,
					SystemPrompt: systemPrompt,
				}
				if hasMaxTokens {
					cfg.MaxTokens = maxTokens
				}

				// 6. Spawn SubTurn
				return spawnSubTurn(ctx, al, parentTS, cfg)
			})

			// Clone the parent's tool registry so subagents can use all
			// tools registered so far (file, web, etc.) but NOT spawn/
			// spawn_status which are added below — preventing recursive
			// subagent spawning.
			subagentManager.SetTools(agent.Tools.Clone())
			if spawnEnabled {
				spawnTool := tools.NewSpawnTool(subagentManager)
				spawnTool.SetSpawner(NewSubTurnSpawner(al))
				currentAgentID := agentID
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})

				agent.Tools.Register(spawnTool)

				// Also register the synchronous subagent tool
				subagentTool := tools.NewSubagentTool(subagentManager)
				subagentTool.SetSpawner(NewSubTurnSpawner(al))
				agent.Tools.Register(subagentTool)
			}
			if spawnStatusEnabled {
				agent.Tools.Register(tools.NewSpawnStatusTool(subagentManager))
			}
		} else if (spawnEnabled || spawnStatusEnabled) && !cfg.Tools.IsToolEnabled("subagent") {
			logger.WarnCF("agent", "spawn/spawn_status tools require subagent to be enabled", nil)
		}
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	ctx = runCtx
	al.mu.Lock()
	al.runCancel = cancel
	al.running.Store(true)
	al.mu.Unlock()
	defer func() {
		cancel()
		al.mu.Lock()
		al.runCancel = nil
		al.running.Store(false)
		al.mu.Unlock()
	}()

	if err := al.ensureHooksInitialized(ctx); err != nil {
		return err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	var queued []bus.InboundMessage
	for al.running.Load() {
		if ctx.Err() != nil {
			return nil
		}
		var msg bus.InboundMessage
		if len(queued) > 0 {
			msg = queued[0]
			queued[0] = bus.InboundMessage{}
			queued = queued[1:]
		} else {
			select {
			case <-ctx.Done():
				return nil
			case next, ok := <-al.bus.InboundChan():
				if !ok {
					return nil
				}
				msg = next
			}
		}
		// Start a goroutine that drains the bus while processMessage is
		// running. Only messages that resolve to the active turn scope are
		// redirected into steering; other requests retain their transport envelope.
		drainCancel := func() {}
		var drainDone <-chan []bus.InboundMessage
		if activeScope, activeAgentID, ok := al.resolveSteeringTarget(msg); ok {
			drainCtx, cancel := context.WithCancel(ctx)
			drainCancel = cancel
			done := make(chan []bus.InboundMessage, 1)
			drainDone = done
			go func() { done <- al.drainBus(drainCtx, ctx, activeScope, activeAgentID, len(queued), true) }()
		}

		// Process message
		func() {
			defer func() {
				if al.channelManager != nil {
					al.channelManager.InvokeTypingStop(msg.Channel, msg.ChatID)
				}
			}()
			// TODO: Re-enable media cleanup after inbound media is properly consumed by the agent.
			// Currently disabled because files are deleted before the LLM can access their content.
			// defer func() {
			// 	if al.mediaStore != nil && msg.MediaScope != "" {
			// 		if releaseErr := al.mediaStore.ReleaseAll(msg.MediaScope); releaseErr != nil {
			// 			logger.WarnCF("agent", "Failed to release media", map[string]any{
			// 				"scope": msg.MediaScope,
			// 				"error": releaseErr.Error(),
			// 			})
			// 		}
			// 	}
			// }()

			drainCanceled := false
			cancelDrain := func() {
				if drainCanceled {
					return
				}
				drainCancel()
				if drainDone != nil {
					queued = append(queued, (<-drainDone)...)
				}
				drainCanceled = true
			}
			defer cancelDrain()

			responseCtx := withResponsePublication(ctx)
			response, err := al.processMessage(responseCtx, msg)
			if err != nil {
				response = "Error processing message: " + al.safeErrorText(err)
			}
			finalResponse := response

			target, targetErr := al.buildContinuationTarget(msg)
			if targetErr != nil {
				logger.WarnCF("agent", "Failed to build steering continuation target",
					map[string]any{
						"channel": msg.Channel,
						"error":   targetErr.Error(),
					})
				return
			}
			if target == nil {
				cancelDrain()
				if finalResponse != "" {
					al.publishResponseWithControls(
						responseCtx,
						msg,
						msg.Channel,
						msg.ChatID,
						finalResponse,
						msg.MessageID,
						&queued,
					)
				}
				return
			}

			for al.pendingSteeringCountForScope(target.SessionKey) > 0 {
				logger.InfoCF("agent", "Continuing queued steering after turn end",
					map[string]any{
						"channel":     target.Channel,
						"chat_id":     target.ChatID,
						"session_key": target.SessionKey,
						"queue_depth": al.pendingSteeringCountForScope(target.SessionKey),
					})

				continued, continueErr := al.Continue(
					bus.WithStreamReplyTo(responseCtx, target.ReplyToMessageID),
					target.SessionKey,
					target.Channel,
					target.ChatID,
				)
				if continueErr != nil {
					logger.WarnCF("agent", "Failed to continue queued steering",
						map[string]any{
							"channel": target.Channel,
							"chat_id": target.ChatID,
							"error":   continueErr.Error(),
						})
					return
				}
				if continued == "" {
					return
				}

				finalResponse = continued
			}

			cancelDrain()

			for al.pendingSteeringCountForScope(target.SessionKey) > 0 {
				logger.InfoCF("agent", "Draining steering queued during turn shutdown",
					map[string]any{
						"channel":     target.Channel,
						"chat_id":     target.ChatID,
						"session_key": target.SessionKey,
						"queue_depth": al.pendingSteeringCountForScope(target.SessionKey),
					})

				continued, continueErr := al.Continue(
					bus.WithStreamReplyTo(responseCtx, target.ReplyToMessageID),
					target.SessionKey,
					target.Channel,
					target.ChatID,
				)
				if continueErr != nil {
					logger.WarnCF("agent", "Failed to continue queued steering after shutdown drain",
						map[string]any{
							"channel": target.Channel,
							"chat_id": target.ChatID,
							"error":   continueErr.Error(),
						})
					return
				}
				if continued == "" {
					finalResponse = ""
					break
				}

				finalResponse = continued
			}

			if finalResponse != "" {
				al.publishResponseWithControls(
					responseCtx,
					msg,
					target.Channel,
					target.ChatID,
					finalResponse,
					target.ReplyToMessageID,
					&queued,
				)
			}
		}()
	}

	return nil
}

// drainBusToSteering consumes inbound messages and redirects messages from the
// active scope into the steering queue. Other requests retain their complete
// transport envelope in FIFO order until the active turn completes.
func (al *AgentLoop) drainBusToSteering(
	ctx context.Context,
	activeScope, activeAgentID string,
	inherited ...int,
) []bus.InboundMessage {
	pending := 0
	if len(inherited) > 0 {
		pending = inherited[0]
	}
	return al.drainBus(ctx, ctx, activeScope, activeAgentID, pending, true)
}

func (al *AgentLoop) drainBus(
	ctx, requestCtx context.Context,
	activeScope, activeAgentID string,
	pending int,
	steer bool,
) []bus.InboundMessage {
	var queued []bus.InboundMessage
	for {
		var msg bus.InboundMessage
		select {
		case <-ctx.Done():
			return queued
		case next, ok := <-al.bus.InboundChan():
			if !ok {
				return queued
			}
			msg = next
		}

		if al.handleActiveReset(requestCtx, msg) {
			continue
		}

		msgScope, _, scopeOK := al.resolveSteeringTarget(msg)
		if !steer || !scopeOK || msgScope != activeScope || isInlineMessage(msg) ||
			strings.Contains(activeScope, ":telegram:inline:user:") {
			if pending+len(queued) >= maxPendingRequests {
				al.rejectBusyRequest(requestCtx, msg)
				continue
			}
			queued = append(queued, msg)
			continue
		}

		// Transcribe audio if needed before steering, so the agent sees text.
		msg, _ = al.transcribeAudioInMessage(requestCtx, msg)

		logger.InfoCF("agent", "Redirecting inbound message to steering queue",
			map[string]any{
				"channel":     msg.Channel,
				"sender_id":   msg.SenderID,
				"content_len": len(msg.Content),
				"scope":       activeScope,
			})

		if err := al.enqueueSteeringMessage(activeScope, activeAgentID, providers.Message{
			Role:    "user",
			Content: msg.Content,
			Media:   append([]string(nil), msg.Media...),
		}); err != nil {
			logger.WarnCF("agent", "Failed to steer message, will be lost",
				map[string]any{
					"error":   err.Error(),
					"channel": msg.Channel,
				})
		}
	}
}

func (al *AgentLoop) rejectBusyRequest(ctx context.Context, msg bus.InboundMessage) {
	if strings.EqualFold(msg.Metadata["observe_only"], "true") || constants.IsInternalChannel(msg.Channel) {
		return
	}
	// A congested answer channel must not keep reset commands behind busy replies.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel: msg.Channel, ChatID: msg.ChatID, ReplyToMessageID: msg.MessageID,
		Content: "The assistant is busy. Please try again shortly.",
	}); err != nil {
		logger.WarnCF(
			"agent",
			"Failed to deliver busy reply",
			map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "error": err.Error()},
		)
	}
}

func (al *AgentLoop) Stop() {
	al.mu.Lock()
	al.running.Store(false)
	cancel := al.runCancel
	al.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (al *AgentLoop) publishResponseIfNeeded(
	ctx context.Context,
	channel, chatID, response string,
	replyIDs ...string,
) {
	if response == "" {
		return
	}

	alreadySent := false
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent != nil {
		if tool, ok := defaultAgent.Tools.Get("message"); ok {
			if mt, ok := tool.(*tools.MessageTool); ok {
				alreadySent = mt.HasSentInRound()
			}
		}
	}

	if alreadySent {
		logger.DebugCF(
			"agent",
			"Skipped outbound (message tool already sent)",
			map[string]any{"channel": channel},
		)
		return
	}

	replyID := ""
	if len(replyIDs) > 0 {
		replyID = replyIDs[0]
	}
	msg := bus.OutboundMessage{
		DeliveryID:       bus.DeliveryID(ctx),
		Channel:          channel,
		ChatID:           chatID,
		Content:          response,
		ReplyToMessageID: replyID,
	}
	var published bool
	var err error
	if publication := publicationFor(ctx); publication != nil && publication.guard != nil {
		published, err = al.bus.PublishOutboundGuarded(ctx, msg, publication.guard.CommitIfCurrent)
	} else {
		err = al.bus.PublishOutbound(ctx, msg)
		published = err == nil
	}
	if err != nil {
		logger.WarnCF(
			"agent",
			"Failed to publish outbound response",
			map[string]any{"channel": channel, "chat_id": chatID, "error": err.Error()},
		)
	}
	if !published {
		return
	}
	logger.InfoCF("agent", "Published outbound response",
		map[string]any{
			"channel":     channel,
			"chat_id":     chatID,
			"content_len": len(response),
		})
}

func (al *AgentLoop) buildContinuationTarget(msg bus.InboundMessage) (*continuationTarget, error) {
	if msg.Channel == "system" {
		return nil, nil
	}
	if isInlineMessage(msg) {
		return nil, nil
	}

	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		return nil, err
	}

	return &continuationTarget{
		SessionKey:       resolveScopeKey(route, msg.SessionKey),
		Channel:          msg.Channel,
		ChatID:           msg.ChatID,
		ReplyToMessageID: msg.MessageID,
	}, nil
}

// Close releases resources held by agent session stores. Call after Stop.
func (al *AgentLoop) Close() {
	al.mu.RLock()
	runtimeStatus := al.runtimeStatus
	al.mu.RUnlock()
	if runtimeStatus != nil {
		runtimeStatus.Close()
	}
	mcpManager := al.mcp.takeManager()

	if mcpManager != nil {
		if err := mcpManager.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
		}
	}

	al.GetRegistry().Close()
	if al.hooks != nil {
		al.hooks.Close()
	}
	if al.eventBus != nil {
		al.eventBus.Close()
	}
}

// MountHook registers an in-process hook on the agent loop.
func (al *AgentLoop) MountHook(reg HookRegistration) error {
	if al == nil || al.hooks == nil {
		return fmt.Errorf("hook manager is not initialized")
	}
	return al.hooks.Mount(reg)
}

// UnmountHook removes a previously registered in-process hook.
func (al *AgentLoop) UnmountHook(name string) {
	if al == nil || al.hooks == nil {
		return
	}
	al.hooks.Unmount(name)
}

// SubscribeEvents registers a subscriber for agent-loop events.
func (al *AgentLoop) SubscribeEvents(buffer int) EventSubscription {
	if al == nil || al.eventBus == nil {
		ch := make(chan Event)
		close(ch)
		return EventSubscription{C: ch}
	}
	return al.eventBus.Subscribe(buffer)
}

// UnsubscribeEvents removes a previously registered event subscriber.
func (al *AgentLoop) UnsubscribeEvents(id uint64) {
	if al == nil || al.eventBus == nil {
		return
	}
	al.eventBus.Unsubscribe(id)
}

// EventDrops returns the number of dropped events for the given kind.
func (al *AgentLoop) EventDrops(kind EventKind) int64 {
	if al == nil || al.eventBus == nil {
		return 0
	}
	return al.eventBus.Dropped(kind)
}

type turnEventScope struct {
	agentID    string
	sessionKey string
	turnID     string
}

func (al *AgentLoop) newTurnEventScope(agentID, sessionKey string) turnEventScope {
	seq := al.turnSeq.Add(1)
	return turnEventScope{
		agentID:    agentID,
		sessionKey: sessionKey,
		turnID:     fmt.Sprintf("%s-turn-%d", agentID, seq),
	}
}

func (ts turnEventScope) meta(iteration int, source, tracePath string) EventMeta {
	return EventMeta{
		AgentID:    ts.agentID,
		TurnID:     ts.turnID,
		SessionKey: ts.sessionKey,
		Iteration:  iteration,
		Source:     source,
		TracePath:  tracePath,
	}
}

func (al *AgentLoop) emitEvent(kind EventKind, meta EventMeta, payload any) {
	evt := Event{
		Kind:    kind,
		Meta:    meta,
		Payload: payload,
	}

	if al == nil || al.eventBus == nil {
		return
	}

	al.logEvent(evt)

	al.eventBus.Emit(evt)
}

func cloneEventArguments(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(args))
	for k, v := range args {
		cloned[k] = v
	}
	return cloned
}

func (al *AgentLoop) hookAbortError(ts *turnState, stage string, decision HookDecision) error {
	reason := decision.Reason
	if reason == "" {
		reason = "hook requested turn abort"
	}

	err := fmt.Errorf("hook aborted turn during %s: %s", stage, reason)
	al.emitEvent(
		EventKindError,
		ts.eventMeta("hooks", "turn.error"),
		ErrorPayload{
			Stage:   "hook." + stage,
			Message: err.Error(),
		},
	)
	return err
}

func hookDeniedToolContent(prefix, reason string) string {
	if reason == "" {
		return prefix
	}
	return prefix + ": " + reason
}

func (al *AgentLoop) logEvent(evt Event) {
	fields := map[string]any{
		"event_kind":  evt.Kind.String(),
		"agent_id":    evt.Meta.AgentID,
		"turn_id":     evt.Meta.TurnID,
		"session_key": evt.Meta.SessionKey,
		"iteration":   evt.Meta.Iteration,
	}

	if evt.Meta.TracePath != "" {
		fields["trace"] = evt.Meta.TracePath
	}
	if evt.Meta.Source != "" {
		fields["source"] = evt.Meta.Source
	}

	switch payload := evt.Payload.(type) {
	case TurnStartPayload:
		fields["channel"] = payload.Channel
		fields["chat_id"] = payload.ChatID
		fields["user_len"] = len(payload.UserMessage)
		fields["media_count"] = payload.MediaCount
	case TurnEndPayload:
		fields["status"] = payload.Status
		fields["iterations_total"] = payload.Iterations
		fields["duration_ms"] = payload.Duration.Milliseconds()
		fields["final_len"] = payload.FinalContentLen
	case LLMRequestPayload:
		fields["model"] = payload.Model
		fields["messages"] = payload.MessagesCount
		fields["tools"] = payload.ToolsCount
		fields["max_tokens"] = payload.MaxTokens
	case LLMDeltaPayload:
		fields["content_delta_len"] = payload.ContentDeltaLen
		fields["reasoning_delta_len"] = payload.ReasoningDeltaLen
	case LLMResponsePayload:
		fields["content_len"] = payload.ContentLen
		fields["tool_calls"] = payload.ToolCalls
		fields["has_reasoning"] = payload.HasReasoning
	case LLMRetryPayload:
		fields["attempt"] = payload.Attempt
		fields["max_retries"] = payload.MaxRetries
		fields["reason"] = payload.Reason
		fields["error"] = payload.Error
		fields["backoff_ms"] = payload.Backoff.Milliseconds()
	case ContextCompressPayload:
		fields["reason"] = payload.Reason
		fields["dropped_messages"] = payload.DroppedMessages
		fields["remaining_messages"] = payload.RemainingMessages
	case SessionSummarizePayload:
		fields["summarized_messages"] = payload.SummarizedMessages
		fields["kept_messages"] = payload.KeptMessages
		fields["summary_len"] = payload.SummaryLen
		fields["omitted_oversized"] = payload.OmittedOversized
	case ToolExecStartPayload:
		fields["tool"] = payload.Tool
		fields["args_count"] = len(payload.Arguments)
	case ToolExecEndPayload:
		fields["tool"] = payload.Tool
		fields["duration_ms"] = payload.Duration.Milliseconds()
		fields["for_llm_len"] = payload.ForLLMLen
		fields["for_user_len"] = payload.ForUserLen
		fields["is_error"] = payload.IsError
		fields["async"] = payload.Async
	case ToolExecSkippedPayload:
		fields["tool"] = payload.Tool
		fields["reason"] = payload.Reason
	case SteeringInjectedPayload:
		fields["count"] = payload.Count
		fields["total_content_len"] = payload.TotalContentLen
	case FollowUpQueuedPayload:
		fields["source_tool"] = payload.SourceTool
		fields["channel"] = payload.Channel
		fields["chat_id"] = payload.ChatID
		fields["content_len"] = payload.ContentLen
	case InterruptReceivedPayload:
		fields["interrupt_kind"] = payload.Kind
		fields["role"] = payload.Role
		fields["content_len"] = payload.ContentLen
		fields["queue_depth"] = payload.QueueDepth
		fields["hint_len"] = payload.HintLen
	case SubTurnSpawnPayload:
		fields["child_agent_id"] = payload.AgentID
		fields["label"] = payload.Label
	case SubTurnEndPayload:
		fields["child_agent_id"] = payload.AgentID
		fields["status"] = payload.Status
	case SubTurnResultDeliveredPayload:
		fields["target_channel"] = payload.TargetChannel
		fields["target_chat_id"] = payload.TargetChatID
		fields["content_len"] = payload.ContentLen
	case ErrorPayload:
		fields["stage"] = payload.Stage
		fields["error"] = payload.Message
	}

	logger.InfoCF("eventbus", fmt.Sprintf("Agent event: %s", evt.Kind.String()), fields)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
}

// ReloadProviderAndConfig atomically swaps the provider and config with proper synchronization.
// It uses a context to allow timeout control from the caller.
// Returns an error if the reload fails or context is canceled.
func (al *AgentLoop) ReloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) error {
	// Validate inputs
	if provider == nil {
		return fmt.Errorf("provider cannot be nil")
	}
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	// Create new registry with updated config and provider
	// Wrap in defer/recover to handle any panics gracefully
	var registry *AgentRegistry
	var panicErr error
	done := make(chan struct{}, 1)
	al.mu.RLock()
	deferModelSelection := al.routingSource != nil
	al.mu.RUnlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = fmt.Errorf("panic during registry creation: %v", r)
				logger.ErrorCF("agent", "Panic during registry creation",
					map[string]any{"panic": r})
			}
			close(done)
		}()

		registry = NewAgentRegistry(cfg, provider, deferModelSelection)
		if ctx.Err() != nil {
			registry.Close()
			registry = nil
		}
	}()

	// Wait for completion or context cancellation
	select {
	case <-done:
		if registry == nil {
			if panicErr != nil {
				return fmt.Errorf("registry creation failed: %w", panicErr)
			}
			return fmt.Errorf("registry creation failed (nil result)")
		}
	case <-ctx.Done():
		return fmt.Errorf("context canceled during registry creation: %w", ctx.Err())
	}

	// Check context again before proceeding
	if err := ctx.Err(); err != nil {
		registry.Close()
		return fmt.Errorf("context canceled after registry creation: %w", err)
	}

	// Ensure shared tools are re-registered on the new registry
	registerSharedTools(al, cfg, al.bus, registry, provider)

	// Atomically swap the config and registry under write lock
	// This ensures readers see a consistent pair
	al.mu.Lock()
	oldRegistry := al.registry

	// Store new values
	al.cfg = cfg
	al.registry = registry

	// Also update fallback chain with new config
	al.fallback = providers.NewFallbackChain(providers.NewCooldownTracker())

	al.mu.Unlock()

	al.hookRuntime.reset(al)
	configureHookManagerFromConfig(al.hooks, cfg)

	// Cancel and join memory workers before closing either their storage or
	// their owning provider. Old generation views remain frozen during cleanup.
	if oldRegistry != nil {
		oldRegistry.Close()
	}
	if oldProvider, ok := extractProvider(oldRegistry); ok {
		if stateful, ok := oldProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
	}

	logger.InfoCF("agent", "Provider and config reloaded successfully",
		map[string]any{
			"model": cfg.Agents.Defaults.GetModelName(),
		})

	return nil
}

// GetRegistry returns the current registry (thread-safe)
func (al *AgentLoop) GetRegistry() *AgentRegistry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.registry
}

// GetConfig returns the current config (thread-safe)
func (al *AgentLoop) GetConfig() *config.Config {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cfg
}

// SetMediaStore injects a MediaStore for media lifecycle management.
func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	al.mediaStore = s

	// Propagate store to send_file tools in all agents.
	registry := al.GetRegistry()
	registry.ForEachTool("send_file", func(t tools.Tool) {
		if sf, ok := t.(*tools.SendFileTool); ok {
			sf.SetMediaStore(s)
		}
	})
}

// SetTranscriber injects a voice transcriber for agent-level audio transcription.
func (al *AgentLoop) SetTranscriber(t voice.Transcriber) {
	al.transcriber = t
}

// SetReloadFunc sets the callback function for triggering config reload.
func (al *AgentLoop) SetReloadFunc(fn func() error) {
	al.reloadFunc = fn
}

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) (bus.InboundMessage, bool) {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, false
	}

	// Transcribe each audio media ref in order. Successfully transcribed audio
	// refs are removed from msg.Media so the model does not see both the
	// transcript and the original file path and then try to inspect the file again.
	var (
		audioReplacements []string
		successTexts      []string
		keptMedia         = make([]string, 0, len(msg.Media))
	)
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			keptMedia = append(keptMedia, ref)
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			audioReplacements = append(audioReplacements, "")
			keptMedia = append(keptMedia, ref)
			continue
		}
		audioReplacements = append(audioReplacements, result.Text)
		successTexts = append(successTexts, result.Text)
	}

	if len(successTexts) == 0 {
		return msg, false
	}

	al.sendTranscriptionFeedback(ctx, msg.Channel, msg.ChatID, msg.MessageID, successTexts)

	// Replace audio annotations sequentially with transcriptions.
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(audioReplacements) {
			return match
		}
		text := audioReplacements[idx]
		idx++
		if text == "" {
			return match
		}
		return "[voice: " + text + "]"
	})

	// Append any remaining transcriptions not matched by an annotation.
	for ; idx < len(audioReplacements); idx++ {
		if audioReplacements[idx] == "" {
			continue
		}
		newContent += "\n[voice: " + audioReplacements[idx] + "]"
	}

	msg.Content = newContent
	msg.Media = keptMedia
	return msg, true
}

// sendTranscriptionFeedback sends feedback to the user with the result of
// audio transcription if the option is enabled. It uses Manager.SendMessage
// which executes synchronously (rate limiting, splitting, retry) so that
// ordering with the subsequent placeholder is guaranteed.
func (al *AgentLoop) sendTranscriptionFeedback(
	ctx context.Context,
	channel, chatID, messageID string,
	validTexts []string,
) {
	if !al.cfg.Voice.EchoTranscription {
		return
	}
	if al.channelManager == nil {
		return
	}

	var nonEmpty []string
	for _, t := range validTexts {
		if t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}

	var feedbackMsg string
	if len(nonEmpty) > 0 {
		feedbackMsg = "Transcript: " + strings.Join(nonEmpty, "\n")
	} else {
		feedbackMsg = "No voice detected in the audio"
	}

	err := al.channelManager.SendMessage(ctx, bus.OutboundMessage{
		Channel:          channel,
		ChatID:           chatID,
		Content:          feedbackMsg,
		ReplyToMessageID: messageID,
	})
	if err != nil {
		logger.WarnCF("voice", "Failed to send transcription feedback", map[string]any{"error": err.Error()})
	}
}

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.
func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	// Fallback: infer from extension
	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return "", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

func (al *AgentLoop) ProcessScheduled(ctx context.Context, req tools.ScheduledRequest) (string, error) {
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return "", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	sender := req.Sender
	senderID := sender.CanonicalID
	if senderID == "" {
		senderID = "cron"
	}
	if sender.DisplayName == "" {
		sender.DisplayName = senderID
	}

	msg := bus.InboundMessage{
		Channel:    req.Channel,
		SenderID:   senderID,
		Sender:     sender,
		ChatID:     req.ChatID,
		Content:    req.Content,
		SessionKey: req.SessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return "", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
	})
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	logger.InfoCF(
		"agent",
		"Processing inbound message",
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
			"content_len": len(msg.Content),
			"media_count": len(msg.Media),
		},
	)

	var hadAudio bool
	msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	observeOnly := strings.EqualFold(strings.TrimSpace(msg.Metadata["observe_only"]), "true")

	if hadAudio && !observeOnly && al.channelManager != nil {
		al.channelManager.SendPlaceholder(bus.WithStreamReplyTo(ctx, msg.MessageID), msg.Channel, msg.ChatID)
	}

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return "", routeErr
	}

	inlineMode := isInlineMessage(msg)
	if !inlineMode {
		observeChatMemoryInbound(ctx, agent, msg)
	}
	agent.Tools.ResetRoundDeliveries()

	// Resolve session key from route, while preserving explicit agent-scoped keys.
	sessionKey, keyErr := al.messageConversationKey(msg, route, agent)
	if keyErr != nil {
		return "", keyErr
	}
	scopeKey := sessionKey

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":      agent.ID,
			"scope_key":     scopeKey,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		})

	opts := processOptions{
		SessionKey:          sessionKey,
		Channel:             msg.Channel,
		ChatID:              msg.ChatID,
		PeerKind:            strings.TrimSpace(msg.Peer.Kind),
		ChatLabel:           strings.TrimSpace(msg.Metadata["chat_label"]),
		Language:            detectMessageLanguageHint(msg.Content),
		ResponseQuote:       inlineResponseQuote(msg),
		Sender:              msg.Sender,
		SenderID:            msg.SenderID,
		SenderDisplayName:   msg.Sender.DisplayName,
		TelegramUserID:      telegramUserID(msg),
		ReplyToMessageID:    msg.MessageID,
		UserMessage:         buildConversationUserMessage(msg),
		Media:               msg.Media,
		DefaultResponse:     defaultResponse,
		EnableSummary:       true,
		SendResponse:        false,
		NoHistory:           false,
		AllowedTools:        nil,
		DisableToolFeedback: inlineMode,
		ConversationContext: ConversationContext{Inline: inlineMode},
	}
	if inlineMode {
		opts.AllowedTools = inlineToolAllowlist()
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		return response, nil
	}
	if observeOnly {
		observedAgent, _, captureErr := captureConversationAgent(agent, opts.SessionKey)
		if captureErr != nil {
			return "", captureErr
		}
		agent = observedAgent
		agent.Sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)
		recordMemoryObservation(
			ctx,
			agent,
			opts.SessionKey,
			opts.Channel,
			opts.ChatID,
			opts.PeerKind,
			opts.ChatLabel,
			"user",
			opts.SenderID,
			opts.UserMessage,
		)
		observedMessages := agent.ContextBuilder.BuildMessages(
			agent.Sessions.GetHistory(opts.SessionKey), agent.Sessions.GetSummary(opts.SessionKey), "", "", nil,
			opts.Channel, opts.ChatID, opts.ChatLabel, opts.SenderID, opts.SenderDisplayName,
			opts.ConversationContext,
		)
		observedMessages = resolveMediaRefs(
			observedMessages,
			al.mediaStore,
			al.GetConfig().Agents.Defaults.GetMaxMediaSize(),
			nil,
		)
		requestTokens := estimateRequestTokens(
			observedMessages,
			agent.Tools.ToProviderDefsFiltered(newToolAllowlist(opts.AllowedTools)),
			agent.MaxTokens,
		)
		al.maybeSummarize(ctx, agent, opts.SessionKey, al.newTurnEventScope(agent.ID, opts.SessionKey), requestTokens)
		logger.DebugCF("agent", "Observed message without response",
			map[string]any{
				"agent_id":    agent.ID,
				"channel":     msg.Channel,
				"chat_id":     msg.ChatID,
				"sender_id":   msg.SenderID,
				"session_key": opts.SessionKey,
			})
		return "", nil
	}

	return al.runAgentLoop(ctx, agent, opts)
}

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()
	route := registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  inboundMetadata(msg, metadataKeyAccountID),
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    inboundMetadata(msg, metadataKeyGuildID),
		TeamID:     inboundMetadata(msg, metadataKeyTeamID),
	})

	agent, ok := registry.GetAgent(route.AgentID)
	if !ok {
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

func resolveScopeKey(route routing.ResolvedRoute, msgSessionKey string) string {
	if msgSessionKey != "" && strings.HasPrefix(msgSessionKey, sessionKeyAgentPrefix) {
		return msgSessionKey
	}
	return route.SessionKey
}

func (al *AgentLoop) messageConversationKey(
	msg bus.InboundMessage,
	route routing.ResolvedRoute,
	agent *AgentInstance,
) (string, error) {
	if isInlineMessage(msg) {
		uid := telegramUserID(msg)
		if uid == "" {
			return "", fmt.Errorf("inline conversation requires a Telegram user identity")
		}
		return inlineConversationKeyForChannel(agent.ID, uid, msg.Channel), nil
	}
	if al.cfg.IsNamedChannel(msg.Channel) && msg.SessionKey != "" &&
		!strings.HasPrefix(msg.SessionKey, "agent:"+agent.ID+":"+msg.Channel+":") {
		return route.SessionKey, nil
	}
	return resolveScopeKey(route, msg.SessionKey), nil
}

func (al *AgentLoop) resolveSteeringTarget(msg bus.InboundMessage) (string, string, bool) {
	if msg.Channel == "system" {
		return "", "", false
	}

	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil || agent == nil {
		return "", "", false
	}

	key, err := al.messageConversationKey(msg, route, agent)
	return key, agent.ID, err == nil
}

func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// Use default agent for system messages
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for system message")
	}

	// Use the origin session for context
	sessionKey := routing.BuildAgentMainSessionKey(agent.ID)

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}

// runAgentLoop remains the top-level shell that starts a turn and publishes
// any post-turn work. runTurn owns the full turn lifecycle.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (response string, err error) {
	defer func() { err = al.publicError(err) }()
	captured, resetAt, err := captureConversationAgent(agent, opts.SessionKey)
	if err != nil {
		return "", err
	}
	agent, err = al.captureRouting(captured)
	if err != nil {
		return "", err
	}
	if publicationFor(ctx) == nil {
		ctx = withResponsePublication(ctx)
	}
	publicationFor(ctx).guard, _ = agent.Sessions.(session.ConversationTurn)
	if !resetAt.IsZero() {
		opts.ConversationContext.Reset = true
		opts.ConversationContext.ResetAt = resetAt
	}

	if opts.ConversationContext.Inline {
		turnAgent := *agent
		turnAgent.MemoryIndex = nil
		turnAgent.ChatMemory = nil
		agent = &turnAgent
	}
	// Record last channel for heartbeat notifications (skip internal channels and cli)
	if opts.Channel != "" && opts.ChatID != "" &&
		!constants.IsInternalChannel(opts.Channel) &&
		!(al.cfg.IsChannelType(opts.Channel, "telegram") && strings.HasPrefix(strings.TrimSpace(opts.ChatID), "inline:")) {
		channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
		if recordErr := al.RecordLastChannel(channelKey); recordErr != nil {
			logger.WarnCF(
				"agent",
				"Failed to record last channel",
				map[string]any{"error": al.safeErrorText(recordErr)},
			)
		}
	}

	ts := newTurnState(agent, opts, al.newTurnEventScope(agent.ID, opts.SessionKey))
	result, err := al.runTurn(ctx, ts)
	if err != nil {
		return "", err
	}
	if result.status == TurnEndStatusAborted {
		return "", nil
	}
	if view, ok := agent.Sessions.(session.ConversationTurn); ok {
		current, err := view.IsCurrent()
		if err != nil || !current {
			return "", err
		}
	}

	for _, followUp := range result.followUps {
		if pubErr := al.bus.PublishInbound(ctx, followUp); pubErr != nil {
			logger.WarnCF("agent", "Failed to publish follow-up after turn",
				map[string]any{
					"turn_id": ts.turnID,
					"error":   pubErr.Error(),
				})
		}
	}

	if opts.SendResponse && result.finalContent != "" {
		al.publishResponseIfNeeded(ctx, opts.Channel, opts.ChatID, result.finalContent, opts.ReplyToMessageID)
	}

	if result.finalContent != "" {
		logger.InfoCF("agent", "Response prepared",
			map[string]any{
				"agent_id":     agent.ID,
				"session_key":  opts.SessionKey,
				"iterations":   ts.currentIteration(),
				"final_length": len(result.finalContent),
			})
	}

	return result.finalContent, nil
}

func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.channelManager == nil {
		return ""
	}
	if ch, ok := al.channelManager.GetChannel(channelName); ok {
		return ch.ReasoningChannelID()
	}
	return ""
}

func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}

	// Check context cancellation before attempting to publish,
	// since PublishOutbound's select may race between send and ctx.Done().
	if ctx.Err() != nil {
		return
	}

	// Use a short timeout so the goroutine does not block indefinitely when
	// the outbound bus is full.  Reasoning output is best-effort; dropping it
	// is acceptable to avoid goroutine accumulation.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channelName,
		ChatID:  channelID,
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// (5 s) while the parent ctx is still active.
		// Also treat ErrBusClosed as expected — it occurs during normal
		// shutdown when the bus is closed before all goroutines finish.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish reasoning (best-effort)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		}
	}
}

func (al *AgentLoop) runTurn(ctx context.Context, ts *turnState) (result turnResult, turnErr error) {
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	ts.setTurnCancel(turnCancel)

	// Inject turnState and AgentLoop into context so tools (e.g. spawn) can retrieve them.
	turnCtx = withTurnState(turnCtx, ts)
	turnCtx = WithAgentLoop(turnCtx, al)
	turnCtx = al.beginRuntimeTurn(turnCtx, ts)

	al.registerActiveTurn(ts)
	defer al.clearActiveTurn(ts)

	turnStatus := TurnEndStatusCompleted
	defer func() {
		if turnErr != nil && turnStatus == TurnEndStatusCompleted {
			turnStatus = TurnEndStatusError
		}
		if ts.runtimeTrace != nil {
			state := "completed"
			if turnStatus == TurnEndStatusError {
				state = "failed"
			}
			if turnStatus == TurnEndStatusAborted || ctx.Err() != nil {
				state = "canceled"
			}
			ts.runtimeTrace.Finish(state)
		}
		al.emitEvent(
			EventKindTurnEnd,
			ts.eventMeta("runTurn", "turn.end"),
			TurnEndPayload{
				Status:          turnStatus,
				Iterations:      ts.currentIteration(),
				Duration:        time.Since(ts.startedAt),
				FinalContentLen: ts.finalContentLen(),
			},
		)
	}()

	if view, ok := ts.agent.Sessions.(session.ConversationTurn); ok {
		current, err := view.IsCurrent()
		if err != nil {
			turnStatus = TurnEndStatusError
			return turnResult{}, err
		}
		if !current {
			ts.requestHardAbort()
			turnStatus = TurnEndStatusAborted
			return al.abortTurn(ts)
		}
	}

	al.emitEvent(
		EventKindTurnStart,
		ts.eventMeta("runTurn", "turn.start"),
		TurnStartPayload{
			Channel:     ts.channel,
			ChatID:      ts.chatID,
			UserMessage: ts.userMessage,
			MediaCount:  len(ts.media),
		},
	)

	var history []providers.Message
	var summary string
	var retrievedMemory string
	maxMediaSize := al.GetConfig().Agents.Defaults.GetMaxMediaSize()
	input, attachmentErr := al.retainAttachments(
		ts,
		providers.Message{Content: ts.userMessage, Media: ts.media},
		maxMediaSize,
	)
	if attachmentErr != nil {
		turnStatus = TurnEndStatusError
		return turnResult{}, attachmentErr
	}
	ts.userMessage, ts.media = input.Content, input.Media
	if !ts.opts.NoHistory {
		assembled, err := contextHistory(turnCtx, ts.agent, ts.sessionKey, 0)
		if err != nil {
			return turnResult{}, err
		}
		history, summary = assembled.History, assembled.Summary
		if !ts.opts.ConversationContext.Inline {
			retrievedMemory = lookupRetrievedMemories(
				ctx,
				ts.agent,
				ts.sessionKey,
				ts.opts.Channel,
				ts.opts.ChatID,
				ts.opts.PeerKind,
				ts.opts.SenderID,
				ts.opts.UserMessage,
				ts.opts.ConversationContext,
			)
		}
	}
	ts.captureRestorePoint(ts.agent.Sessions.GetHistory(ts.sessionKey), ts.agent.Sessions.GetSummary(ts.sessionKey))

	messages := ts.agent.ContextBuilder.BuildMessages(
		history,
		summary,
		retrievedMemory,
		ts.userMessage,
		ts.media,
		ts.channel,
		ts.chatID,
		ts.opts.ChatLabel,
		ts.opts.SenderID,
		ts.opts.SenderDisplayName,
		ts.opts.ConversationContext,
	)

	cfg := al.GetConfig()
	if ts.agent.requestRouting != nil {
		cfg = ts.agent.requestRouting.cfg
	}
	messages = al.resolveTurnMedia(turnCtx, ts, messages, maxMediaSize)
	allowedTools := newToolAllowlist(ts.opts.AllowedTools)

	if !ts.opts.NoHistory {
		toolDefs := ts.agent.Tools.ToProviderDefsFiltered(allowedTools)
		for isOverContextBudget(ts.agent.ContextWindow, messages, toolDefs, ts.agent.MaxTokens) {
			logger.WarnCF("agent", "Proactive compression: context budget exceeded before LLM call",
				map[string]any{"session_key": ts.sessionKey})
			compression, ok, err := al.compressContext(
				turnCtx,
				ts.agent,
				ts.sessionKey,
				0,
				messages,
				toolDefs,
				ts.agent.MaxTokens,
			)
			if err != nil {
				return turnResult{}, err
			}
			if !ok || compression.DroppedMessages <= 0 {
				break
			}
			al.emitEvent(
				EventKindContextCompress,
				ts.eventMeta("runTurn", "turn.context.compress"),
				ContextCompressPayload{
					Reason:            ContextCompressReasonProactive,
					DroppedMessages:   compression.DroppedMessages,
					RemainingMessages: compression.RemainingMessages,
				},
			)
			ts.refreshRestorePointFromSession(ts.agent)
			assembled, err := contextHistory(turnCtx, ts.agent, ts.sessionKey, 0)
			if err != nil {
				return turnResult{}, err
			}
			newHistory, newSummary := assembled.History, assembled.Summary
			messages = ts.agent.ContextBuilder.BuildMessages(
				newHistory, newSummary, retrievedMemory, ts.userMessage,
				ts.media, ts.channel, ts.chatID, ts.opts.ChatLabel,
				ts.opts.SenderID, ts.opts.SenderDisplayName,
				ts.opts.ConversationContext,
			)
			messages = al.resolveTurnMedia(turnCtx, ts, messages, maxMediaSize)
		}
	}

	// Save user message to session (from Incoming)
	if !ts.opts.NoHistory && (strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0) {
		rootMsg := providers.Message{
			Role:    "user",
			Content: ts.userMessage,
			Media:   append([]string(nil), ts.media...),
		}
		rootMsg.SourceID = ts.opts.ReplyToMessageID
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, rootMsg)
		ts.recordPersistedMessage(rootMsg)
		recordMemoryObservation(
			ctx,
			ts.agent,
			ts.sessionKey,
			ts.channel,
			ts.chatID,
			ts.opts.PeerKind,
			ts.opts.ChatLabel,
			"user",
			ts.opts.SenderID,
			ts.userMessage,
		)
	}

	activeProvider, activeCandidates := ts.agent.Provider, ts.agent.Candidates
	activeModel := resolvedCandidateModel(activeCandidates, ts.agent.Model)
	if !cfg.IsChannelType(ts.channel, "pico") || len(ts.media) == 0 {
		activeProvider, activeCandidates, activeModel = al.selectCandidates(ts.agent, ts.userMessage, messages)
	}
	resolveCandidateProvider := candidateProviderResolver(cfg, ts.agent.Workspace, activeProvider, activeCandidates)

	pendingMessages := append([]providers.Message(nil), ts.opts.InitialSteeringMessages...)
	var finalContent string
	var finalModelContent string
	var lastResponseModel string
	var finalContentPhase providers.ContentPhase
	var lastToolResultForFallback string
	var hadToolCalls bool
	var terminalToolCompleted bool
	lastRequestTokens := estimateRequestTokens(
		messages,
		ts.agent.Tools.ToProviderDefsFiltered(allowedTools),
		ts.agent.MaxTokens,
	)

turnLoop:
	for ts.currentIteration() < ts.agent.MaxIterations || len(pendingMessages) > 0 || func() bool {
		graceful, _ := ts.gracefulInterruptRequested()
		return graceful
	}() {
		if ts.hardAbortRequested() {
			turnStatus = TurnEndStatusAborted
			return al.abortTurn(ts)
		}

		iteration := ts.currentIteration() + 1
		ts.setIteration(iteration)
		ts.setPhase(TurnPhaseRunning)

		if iteration > 1 {
			if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
				pendingMessages = append(pendingMessages, steerMsgs...)
			}
		} else if !ts.opts.SkipInitialSteeringPoll {
			if steerMsgs := al.dequeueSteeringMessagesForScopeWithFallback(ts.sessionKey); len(steerMsgs) > 0 {
				pendingMessages = append(pendingMessages, steerMsgs...)
			}
		}

		// Check if parent turn has ended (SubTurn support from HEAD)
		if ts.parentTurnState != nil && ts.IsParentEnded() {
			if !ts.critical {
				logger.InfoCF("agent", "Parent turn ended, non-critical SubTurn exiting gracefully", map[string]any{
					"agent_id":  ts.agentID,
					"iteration": iteration,
					"turn_id":   ts.turnID,
				})
				break
			}
			logger.InfoCF("agent", "Parent turn ended, critical SubTurn continues running", map[string]any{
				"agent_id":  ts.agentID,
				"iteration": iteration,
				"turn_id":   ts.turnID,
			})
		}

		// Poll for pending SubTurn results (from HEAD)
		if ts.pendingResults != nil {
			select {
			case result, ok := <-ts.pendingResults:
				if ok && result != nil && result.ForLLM != "" {
					content := al.cfg.FilterSensitiveData(result.ForLLM)
					msg := providers.Message{Role: "user", Content: fmt.Sprintf("[SubTurn Result] %s", content)}
					pendingMessages = append(pendingMessages, msg)
				}
			default:
				// No results available
			}
		}

		// Inject pending steering messages
		if len(pendingMessages) > 0 {
			for i := range pendingMessages {
				retained, retainErr := al.retainAttachments(ts, pendingMessages[i], maxMediaSize)
				if retainErr != nil {
					return turnResult{}, retainErr
				}
				pendingMessages[i] = retained
			}
			// Include existing context when deciding whether steering attachments fit.
			combined := append(append([]providers.Message(nil), messages...), pendingMessages...)
			resolvedPending := al.resolveTurnMedia(turnCtx, ts, combined, maxMediaSize)[len(messages):]
			totalContentLen := 0
			for i, pm := range pendingMessages {
				messages = append(messages, resolvedPending[i])
				totalContentLen += len(pm.Content)
				if !ts.opts.NoHistory {
					ts.agent.Sessions.AddFullMessage(ts.sessionKey, pm)
					ts.recordPersistedMessage(pm)
				}
				logger.InfoCF("agent", "Injected steering message into context",
					map[string]any{
						"agent_id":    ts.agent.ID,
						"iteration":   iteration,
						"content_len": len(pm.Content),
						"media_count": len(pm.Media),
					})
			}
			al.emitEvent(
				EventKindSteeringInjected,
				ts.eventMeta("runTurn", "turn.steering.injected"),
				SteeringInjectedPayload{
					Count:           len(pendingMessages),
					TotalContentLen: totalContentLen,
				},
			)
			pendingMessages = nil
		}

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"max":       ts.agent.MaxIterations,
			})

		gracefulTerminal, _ := ts.gracefulInterruptRequested()
		providerToolDefs := ts.agent.Tools.ToProviderDefsFiltered(allowedTools)

		callMessages := messages
		if gracefulTerminal {
			callMessages = append(append([]providers.Message(nil), messages...), ts.interruptHintMessage())
			providerToolDefs = nil
			ts.markGracefulTerminalUsed()
		}
		ts.mu.RLock()
		protectedStart := max(1, len(messages)-len(ts.persistedMessages))
		ts.mu.RUnlock()
		if ts.opts.NoHistory {
			protectedStart = 1
		}

		llmOpts := map[string]any{
			"max_tokens":       ts.agent.MaxTokens,
			"temperature":      ts.agent.Temperature,
			"prompt_cache_key": ts.agent.ID,
		}

		llmModel := activeModel
		if al.hooks != nil {
			llmReq, decision := al.hooks.BeforeLLM(turnCtx, &LLMHookRequest{
				Meta:             ts.eventMeta("runTurn", "turn.llm.request"),
				Model:            llmModel,
				Messages:         callMessages,
				Tools:            providerToolDefs,
				Options:          llmOpts,
				Channel:          ts.channel,
				ChatID:           ts.chatID,
				GracefulTerminal: gracefulTerminal,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if llmReq != nil {
					llmModel = llmReq.Model
					callMessages = llmReq.Messages
					providerToolDefs = llmReq.Tools
					llmOpts = llmReq.Options
				}
			case HookActionAbortTurn:
				turnStatus = TurnEndStatusError
				return turnResult{}, al.hookAbortError(ts, "before_llm", decision)
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}
		}

		al.emitEvent(
			EventKindLLMRequest,
			ts.eventMeta("runTurn", "turn.llm.request"),
			LLMRequestPayload{
				Model:         llmModel,
				MessagesCount: len(callMessages),
				ToolsCount:    len(providerToolDefs),
				MaxTokens:     ts.agent.MaxTokens,
				Temperature:   ts.agent.Temperature,
			},
		)

		logger.DebugCF("agent", "LLM request",
			map[string]any{
				"agent_id":          ts.agent.ID,
				"iteration":         iteration,
				"model":             llmModel,
				"messages_count":    len(callMessages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        ts.agent.MaxTokens,
				"temperature":       ts.agent.Temperature,
				"system_prompt_len": len(callMessages[0].Content),
			})
		logger.DebugCF("agent", "Full LLM request",
			map[string]any{
				"iteration":     iteration,
				"messages_json": formatMessagesForLog(callMessages),
				"tools_json":    formatToolsForLog(providerToolDefs),
			})

		modelAttempts := 0
		modelBudgetAttempts := 0
		retryAnnounced := false
		var lastCandidateFailure *providers.FailoverError
		callLLM := func(messagesForCall []providers.Message, toolDefsForCall []providers.ToolDefinition) (*providers.LLMResponse, error) {
			providerCtx, providerCancel := context.WithCancel(bus.WithStreamReplyTo(turnCtx, ts.opts.ReplyToMessageID))
			ts.setProviderCancel(providerCancel)
			defer func() {
				providerCancel()
				ts.clearProviderCancel(providerCancel)
			}()

			al.activeRequests.Add(1)
			defer al.activeRequests.Done()

			callProviderOnce := func(ctx context.Context, client providers.LLMProvider, candidate providers.FallbackCandidate) (reply *providers.LLMResponse, requestErr error) {
				model := candidate.Model
				attemptLimit := candidateInactivityTimeout(cfg, candidate, ts.agent.Workspace)
				candidateDefs, candidateOpts := prepareCandidateRequest(cfg, client, candidate, toolDefsForCall, llmOpts, ts.agent.ThinkingLevel)
				if err := checkCandidateContext(cfg, candidate, client, messagesForCall, candidateDefs, candidateOpts, ts.agent.ContextWindow, ts.agent.MaxTokens); err != nil {
					al.emitEvent(EventKindLLMRetry, ts.eventMeta("runTurn", "turn.llm.candidate_skipped"), LLMRetryPayload{Reason: "incompatible", Error: err.Error()})
					logger.WarnCF("agent", "Skipping incompatible model candidate", map[string]any{"model": candidate.Model, "config_name": candidate.ConfigName, "reason": err.Error()})
					return nil, &providers.FailoverError{Reason: providers.FailoverIncompatible, Wrapped: err, Candidate: candidate, Provider: candidate.Provider, Model: candidate.Model}
				}
				lastResponseModel = model
				defer func() {
					if failure := providers.ClassifyError(requestErr, "", ""); failure != nil && failure.Status == 503 {
						modelBudgetAttempts--
					}
				}()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// The scheduler owns complete candidate passes and 503 recovery rounds.
				// Only the outer ordinary/empty/context retries consume this budget.
				modelAttempts++
				modelBudgetAttempts++
				reasoningActionID := fmt.Sprintf("reasoning:%d:%d", iteration, modelAttempts)
				streamProvider, providerCanStream := client.(providers.StreamingProvider)
				phasedProvider, providerCanStreamPhases := client.(providers.PhasedStreamingProvider)
				var streamer bus.Streamer
				var streamUpdater *partialReplyUpdater
				streamingEnabled := (providerCanStream || providerCanStreamPhases) &&
					!ts.opts.NoHistory && !constants.IsInternalChannel(ts.channel)
				if streamingEnabled {
					// Each candidate gets its own preview; failed attempts cancel theirs.
					providers.WithoutModelInactivity(ctx, func() {
						streamer, streamUpdater = selectStreamingTargets(ctx, al.bus, al.channelManager, ts.channel, ts.chatID)
					})
				}
				if streamingEnabled && (streamer != nil || streamUpdater != nil) {
					lastSnapshots := make(map[providers.ContentPhase]string)
					var lastReasoningAt time.Time
					var response *providers.LLMResponse
					var err error
					onUpdate := func(update providers.StreamContent) {
						accumulated := update.Content
						contentDeltaLen := len(accumulated) - len(lastSnapshots[update.Phase])
						if contentDeltaLen < 0 {
							contentDeltaLen = len(accumulated)
						}
						lastSnapshots[update.Phase] = accumulated
						deltaPayload := LLMDeltaPayload{ContentPhase: update.Phase}
						if update.Phase == providers.ContentPhaseReasoning {
							deltaPayload.ReasoningDeltaLen = contentDeltaLen
							if time.Since(lastReasoningAt) >= streamUpdateInterval {
								lastReasoningAt = time.Now()
								al.publishWebReasoning(ctx, ts, reasoningActionID, bus.ProgressStarted, accumulated)
							}
						} else {
							deltaPayload.ContentDeltaLen = contentDeltaLen
						}
						al.emitEvent(
							EventKindLLMDelta,
							ts.eventMeta("runTurn", "turn.llm.delta"),
							deltaPayload,
						)
						// Chat Completions content is public text without phase metadata.
						if update.Phase == providers.ContentPhaseAnswer ||
							(!providerCanStreamPhases && update.Phase == providers.ContentPhaseUnknown) {
							offerStreamingContent(ctx, streamer, streamUpdater, formatInlineResponse(ts.opts.ResponseQuote, accumulated))
						}
					}
					if providerCanStreamPhases {
						response, err = providers.RunModelAttempt(ctx, attemptLimit, func(attemptCtx context.Context) (*providers.LLMResponse, error) {
							return phasedProvider.ChatStreamContent(attemptCtx, messagesForCall, candidateDefs, model, candidateOpts, func(update providers.StreamContent) {
								providers.WithoutModelInactivity(attemptCtx, func() { onUpdate(update) })
							})
						})
					} else {
						response, err = providers.RunModelAttempt(ctx, attemptLimit, func(attemptCtx context.Context) (*providers.LLMResponse, error) {
							return streamProvider.ChatStream(attemptCtx, messagesForCall, candidateDefs, model, candidateOpts, func(content string) {
								providers.WithoutModelInactivity(attemptCtx, func() { onUpdate(providers.StreamContent{Content: content}) })
							})
						})
					}
					if err != nil {
						al.publishWebReasoning(ctx, ts, reasoningActionID, bus.ProgressFailed, lastSnapshots[providers.ContentPhaseReasoning])
						cancelStreamingContent(ctx, streamer)
						return nil, err
					}
					if response == nil {
						cancelStreamingContent(ctx, streamer)
						return nil, errEmptyAnswer
					}
					al.publishWebReasoning(ctx, ts, reasoningActionID, bus.ProgressCompleted, lastSnapshots[providers.ContentPhaseReasoning])
					if len(response.ToolCalls) > 0 || !isAnswerContent(response) {
						cancelStreamingContent(ctx, streamer)
					}
					if len(response.ToolCalls) == 0 && isAnswerContent(response) {
						offerStreamingContent(ctx, streamer, streamUpdater, formatInlineResponse(ts.opts.ResponseQuote, response.Content))
						if err := finalizeStreamingContent(ctx, streamer, streamUpdater, formatInlineResponse(ts.opts.ResponseQuote, response.Content)); err != nil {
							logger.WarnCF("agent", "Stream finalize failed", map[string]any{
								"error": err.Error(),
							})
						}
					}
					return response, nil
				}
				return providers.RunModelAttempt(ctx, attemptLimit, func(attemptCtx context.Context) (*providers.LLMResponse, error) {
					return client.Chat(attemptCtx, messagesForCall, candidateDefs, model, candidateOpts)
				})
			}
			callProvider := func(ctx context.Context, client providers.LLMProvider, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
				return callProviderOnce(ctx, client, candidate)
			}
			ts.runtimeTrace.CaptureChain(routingIdentities(activeCandidates))
			candidates := ts.agent.requestRouting.prefer(activeCandidates)
			if len(candidates) == 0 {
				candidates = []providers.FallbackCandidate{{Model: llmModel}}
			} else if llmModel != activeModel {
				candidates = append([]providers.FallbackCandidate(nil), candidates...)
				candidates[0].Model = llmModel
			}
			runCandidate := func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
				client := activeProvider
				if len(activeCandidates) > 0 {
					var err error
					client, err = resolveCandidateProvider(ctx, candidate)
					if err != nil {
						return nil, err
					}
				}
				if stateful, owned := client.(providers.StatefulProvider); owned && client != activeProvider {
					defer stateful.Close()
				}
				response, err := callProvider(ctx, client, candidate)
				lastCandidateFailure = providers.ClassifyCandidateError(err, candidate)
				return response, err
			}
			for i := range candidates {
				candidates[i].InactivityTimeout = candidateInactivityTimeout(cfg, candidates[i], ts.agent.Workspace)
			}
			var fbResult *providers.FallbackResult
			var fbErr error
			if lastCandidateFailure != nil && lastCandidateFailure.IsTransient() && lastCandidateFailure.Status != 503 {
				fbResult, fbErr = al.fallback.RetryCandidate(providerCtx, lastCandidateFailure, runCandidate)
			} else {
				waits := 0
				fbResult, fbErr = al.fallback.ExecuteRecoveryCandidate(providerCtx, candidates, runCandidate, func(delay time.Duration, err error) {
					retryAnnounced = true
					waits++
					al.reportModelRetry(providerCtx, ts, waits, "service_unavailable", err, delay)
				})
			}
			if fbErr != nil {
				return nil, fbErr
			}
			ts.agent.requestRouting.served(fbResult.Candidate)
			return fbResult.Response, nil
		}

		var response *providers.LLMResponse
		var err error
		maxRetries := modelMaxAttempts - 1
		waited := time.Duration(0)
		emptyRecoveryUsed := false
		for retry := 0; retry <= maxRetries; retry++ {
			outputReserve := ts.agent.MaxTokens
			if configured, ok := llmOpts["max_tokens"].(int); ok {
				outputReserve = configured
			}
			if retry == 0 {
				lastRequestTokens = estimateRequestTokens(callMessages, providerToolDefs, outputReserve)
			}
			prepared, dropped, fits := trimContextRequest(callMessages, providerToolDefs, outputReserve, ts.agent.ContextWindow, protectedStart)
			callMessages = prepared
			protectedStart -= dropped
			if !fits {
				err = &contextBudgetError{estimated: estimateRequestTokens(callMessages, providerToolDefs, outputReserve), limit: ts.agent.ContextWindow}
				break
			}
			if dropped > 0 {
				al.emitEvent(EventKindContextCompress, ts.eventMeta("runTurn", "turn.context.compress"), ContextCompressPayload{
					Reason: ContextCompressReasonProactive, DroppedMessages: dropped, RemainingMessages: len(callMessages) - 1,
				})
			}
			response, err = callLLM(callMessages, providerToolDefs)
			if err == nil || errors.Is(err, errEmptyAnswer) {
				if hasModelOutcome(response) {
					break
				}
				err = errEmptyAnswer
				if !emptyRecoveryUsed && retry < maxRetries && modelBudgetAttempts < modelMaxAttempts {
					emptyRecoveryUsed = true
					retryAnnounced = true
					al.reportModelRetry(turnCtx, ts, modelBudgetAttempts, "empty_answer", err, 0)
					continue
				}
				break
			}
			if ts.hardAbortRequested() && errors.Is(err, context.Canceled) {
				if retryAnnounced {
					al.finishModelRetry(turnCtx, ts, context.Canceled)
				}
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}

			if turnCtx.Err() != nil {
				err = turnCtx.Err()
				break
			}
			errMsg := strings.ToLower(err.Error())
			isTimeoutError := errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(errMsg, "deadline exceeded") ||
				strings.Contains(errMsg, "client.timeout") ||
				strings.Contains(errMsg, "timed out") ||
				strings.Contains(errMsg, "timeout exceeded")

			isContextError := !isTimeoutError && (strings.Contains(errMsg, "context_length_exceeded") ||
				strings.Contains(errMsg, "context window") ||
				strings.Contains(errMsg, "maximum context length") ||
				strings.Contains(errMsg, "token limit") ||
				strings.Contains(errMsg, "too many tokens") ||
				strings.Contains(errMsg, "max_tokens") ||
				strings.Contains(errMsg, "invalidparameter") ||
				strings.Contains(errMsg, "prompt is too long") ||
				strings.Contains(errMsg, "request too large"))

			delay, reason, isTransient := modelRetryDelay(err, retry, waited)
			if isTransient && retry < maxRetries && modelBudgetAttempts < modelMaxAttempts {
				retryAnnounced = true
				al.reportModelRetry(turnCtx, ts, modelBudgetAttempts, reason, err, delay)
				if sleepErr := sleepWithContext(turnCtx, delay); sleepErr != nil {
					if ts.hardAbortRequested() {
						if retryAnnounced {
							al.finishModelRetry(turnCtx, ts, context.Canceled)
						}
						turnStatus = TurnEndStatusAborted
						return al.abortTurn(ts)
					}
					err = sleepErr
					break
				}
				waited += delay
				continue
			}

			if isContextError && retry < maxRetries && modelBudgetAttempts < modelMaxAttempts && !ts.opts.NoHistory {
				logger.WarnCF(
					"agent",
					"Context window error detected, attempting compression",
					map[string]any{
						"error": al.safeErrorText(publicProviderError(err)),
						"retry": retry,
					},
				)

				retryAnnounced = true
				al.reportModelRetry(turnCtx, ts, modelBudgetAttempts, "context_limit", err, 0)

				ts.mu.RLock()
				protectedMessages := len(ts.persistedMessages)
				ts.mu.RUnlock()
				compression, ok, compressErr := al.compressContext(turnCtx, ts.agent, ts.sessionKey, protectedMessages, messages, providerToolDefs, outputReserve)
				if compressErr != nil {
					return turnResult{}, compressErr
				}
				if ok {
					al.emitEvent(
						EventKindContextCompress,
						ts.eventMeta("runTurn", "turn.context.compress"),
						ContextCompressPayload{
							Reason:            ContextCompressReasonRetry,
							DroppedMessages:   compression.DroppedMessages,
							RemainingMessages: compression.RemainingMessages,
						},
					)
					ts.refreshRestorePointFromSession(ts.agent)
				}

				assembled, assembleErr := contextHistory(turnCtx, ts.agent, ts.sessionKey, protectedMessages)
				if assembleErr != nil {
					return turnResult{}, assembleErr
				}
				newHistory, newSummary := assembled.History, assembled.Summary
				messages = ts.agent.ContextBuilder.BuildMessages(
					newHistory, newSummary, retrievedMemory, "",
					nil, ts.channel, ts.chatID, ts.opts.ChatLabel,
					ts.opts.SenderID, ts.opts.SenderDisplayName,
					ts.opts.ConversationContext,
				)
				messages = al.resolveTurnMedia(turnCtx, ts, messages, maxMediaSize)
				ts.mu.RLock()
				protectedStart = max(1, len(messages)-len(ts.persistedMessages))
				ts.mu.RUnlock()
				callMessages = messages
				if gracefulTerminal {
					callMessages = append(append([]providers.Message(nil), messages...), ts.interruptHintMessage())
				}
				lastRequestTokens = estimateRequestTokens(callMessages, providerToolDefs, outputReserve)
				continue
			}
			break
		}

		if retryAnnounced {
			al.finishModelRetry(turnCtx, ts, err)
		}

		if err != nil {
			turnStatus = TurnEndStatusError
			publicErr := publicProviderError(err)
			al.emitEvent(
				EventKindError,
				ts.eventMeta("runTurn", "turn.error"),
				ErrorPayload{
					Stage:   "llm",
					Message: al.safeErrorText(publicErr),
				},
			)
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  ts.agent.ID,
					"iteration": iteration,
					"model":     llmModel,
					"error":     al.safeErrorText(publicErr),
				})
			return turnResult{}, publicErr
		}

		if al.hooks != nil {
			llmResp, decision := al.hooks.AfterLLM(turnCtx, &LLMHookResponse{
				Meta:     ts.eventMeta("runTurn", "turn.llm.response"),
				Model:    llmModel,
				Response: response,
				Channel:  ts.channel,
				ChatID:   ts.chatID,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if llmResp != nil && llmResp.Response != nil {
					response = llmResp.Response
				}
			case HookActionAbortTurn:
				turnStatus = TurnEndStatusError
				return turnResult{}, al.hookAbortError(ts, "after_llm", decision)
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}
		}

		// Save finishReason to turnState for SubTurn truncation detection
		if innerTS := turnStateFromContext(ctx); innerTS != nil {
			innerTS.SetLastFinishReason(response.FinishReason)
			// Save usage for token budget tracking
			if response.Usage != nil {
				innerTS.SetLastUsage(response.Usage)
			}
		}

		reasoningContent := response.ReasoningContent
		if reasoningContent == "" {
			reasoningContent = response.Reasoning
		}
		al.publishWebReasoning(turnCtx, ts, fmt.Sprintf("reasoning:%d:%d", iteration, modelAttempts), bus.ProgressCompleted, reasoningContent)
		go al.handleReasoning(
			turnCtx,
			response.Reasoning,
			ts.channel,
			al.targetReasoningChannelID(ts.channel),
		)
		al.emitEvent(
			EventKindLLMResponse,
			ts.eventMeta("runTurn", "turn.llm.response"),
			LLMResponsePayload{
				ContentLen:   len(response.Content),
				ToolCalls:    len(response.ToolCalls),
				HasReasoning: response.Reasoning != "" || response.ReasoningContent != "",
			},
		)

		logger.DebugCF("agent", "LLM response",
			map[string]any{
				"agent_id":       ts.agent.ID,
				"iteration":      iteration,
				"content_chars":  len(response.Content),
				"tool_calls":     len(response.ToolCalls),
				"reasoning":      response.Reasoning,
				"target_channel": al.targetReasoningChannelID(ts.channel),
				"channel":        ts.channel,
			})

		if len(response.ToolCalls) == 0 || gracefulTerminal {
			responseContent := response.Content
			if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
				logger.InfoCF("agent", "Steering arrived after direct LLM response; continuing turn",
					map[string]any{
						"agent_id":       ts.agent.ID,
						"iteration":      iteration,
						"steering_count": len(steerMsgs),
					})
				pendingMessages = append(pendingMessages, steerMsgs...)
				continue
			}
			finalModelContent = responseContent
			finalContent = formatInlineResponse(ts.opts.ResponseQuote, responseContent)
			finalContentPhase = response.ContentPhase
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]any{
					"agent_id":      ts.agent.ID,
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ContentPhase:     response.ContentPhase,
			ReasoningContent: response.ReasoningContent,
			ModelName:        lastResponseModel,
		}
		hadToolCalls = true
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			extraContent := tc.ExtraContent
			thoughtSignature := ""
			if tc.Function != nil {
				thoughtSignature = tc.Function.ThoughtSignature
			}
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Name: tc.Name,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: thoughtSignature,
				},
				ExtraContent:     extraContent,
				ThoughtSignature: thoughtSignature,
			})
		}
		messages = append(messages, assistantMsg)
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, assistantMsg)
			ts.recordPersistedMessage(assistantMsg)
		}

		ts.setPhase(TurnPhaseTools)
		for i, tc := range normalizedToolCalls {
			if ts.hardAbortRequested() {
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}

			toolName := tc.Name
			toolArgs := cloneStringAnyMap(tc.Arguments)

			if al.hooks != nil {
				toolReq, decision := al.hooks.BeforeTool(turnCtx, &ToolCallHookRequest{
					Meta:      ts.eventMeta("runTurn", "turn.tool.before"),
					Tool:      toolName,
					Arguments: toolArgs,
					Channel:   ts.channel,
					ChatID:    ts.chatID,
				})
				switch decision.normalizedAction() {
				case HookActionContinue, HookActionModify:
					if toolReq != nil {
						toolName = toolReq.Tool
						toolArgs = toolReq.Arguments
					}
				case HookActionDenyTool:
					denyContent := hookDeniedToolContent("Tool execution denied by hook", decision.Reason)
					al.emitEvent(
						EventKindToolExecSkipped,
						ts.eventMeta("runTurn", "turn.tool.skipped"),
						ToolExecSkippedPayload{
							Tool:   toolName,
							Reason: denyContent,
						},
					)
					deniedMsg := providers.Message{
						Role:       "tool",
						Content:    denyContent,
						ToolCallID: tc.ID,
					}
					messages = append(messages, deniedMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
						ts.recordPersistedMessage(deniedMsg)
					}
					continue
				case HookActionAbortTurn:
					turnStatus = TurnEndStatusError
					return turnResult{}, al.hookAbortError(ts, "before_tool", decision)
				case HookActionHardAbort:
					_ = ts.requestHardAbort()
					turnStatus = TurnEndStatusAborted
					return al.abortTurn(ts)
				}
			}

			if al.hooks != nil {
				approval := al.hooks.ApproveTool(turnCtx, &ToolApprovalRequest{
					Meta:      ts.eventMeta("runTurn", "turn.tool.approve"),
					Tool:      toolName,
					Arguments: toolArgs,
					Channel:   ts.channel,
					ChatID:    ts.chatID,
				})
				if !approval.Approved {
					denyContent := hookDeniedToolContent("Tool execution denied by approval hook", approval.Reason)
					al.emitEvent(
						EventKindToolExecSkipped,
						ts.eventMeta("runTurn", "turn.tool.skipped"),
						ToolExecSkippedPayload{
							Tool:   toolName,
							Reason: denyContent,
						},
					)
					deniedMsg := providers.Message{
						Role:       "tool",
						Content:    denyContent,
						ToolCallID: tc.ID,
					}
					messages = append(messages, deniedMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
						ts.recordPersistedMessage(deniedMsg)
					}
					continue
				}
			}

			argsJSON, _ := json.Marshal(toolArgs)
			argsPreview := utils.Truncate(string(argsJSON), 200)
			logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", toolName, argsPreview),
				map[string]any{
					"agent_id":  ts.agent.ID,
					"tool":      toolName,
					"iteration": iteration,
				})
			al.emitEvent(
				EventKindToolExecStart,
				ts.eventMeta("runTurn", "turn.tool.start"),
				ToolExecStartPayload{
					Tool:      toolName,
					Arguments: cloneEventArguments(toolArgs),
				},
			)

			actionID := fmt.Sprintf("tool:%d:%d:%s", iteration, i, tc.ID)
			progressEnabled := al.toolProgressEnabled(ts)
			if progressEnabled {
				al.publishToolProgress(turnCtx, ts, actionID, toolName, toolArgs, nil)
			}

			toolCallID := tc.ID
			toolIteration := iteration
			asyncToolName := toolName
			asyncCallback := func(_ context.Context, result *tools.ToolResult) {
				if result == nil {
					return
				}
				result = al.normalizeToolResult(result)
				if progressEnabled {
					al.publishToolProgress(context.Background(), ts, actionID, asyncToolName, toolArgs, result)
				}
				// Send ForUser content directly to the user (immediate feedback),
				// mirroring the synchronous tool execution path.
				if !progressEnabled && !result.Silent && result.ForUser != "" {
					outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer outCancel()
					_ = al.bus.PublishOutbound(outCtx, bus.OutboundMessage{
						Channel: ts.channel,
						ChatID:  ts.chatID,
						Content: result.ForUser,
					})
				}

				// Determine content for the agent loop (ForLLM or error).
				content := result.ForLLM
				if content == "" && result.Err != nil {
					content = result.Err.Error()
				}
				if content == "" {
					return
				}

				// Filter sensitive data before publishing
				content = al.cfg.FilterSensitiveData(content)

				logger.InfoCF("agent", "Async tool completed, publishing result",
					map[string]any{
						"tool":        asyncToolName,
						"content_len": len(content),
						"channel":     ts.channel,
					})
				al.emitEvent(
					EventKindFollowUpQueued,
					ts.scope.meta(toolIteration, "runTurn", "turn.follow_up.queued"),
					FollowUpQueuedPayload{
						SourceTool: asyncToolName,
						Channel:    ts.channel,
						ChatID:     ts.chatID,
						ContentLen: len(content),
					},
				)

				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				_ = al.bus.PublishInbound(pubCtx, bus.InboundMessage{
					Channel:  "system",
					SenderID: fmt.Sprintf("async:%s", asyncToolName),
					ChatID:   fmt.Sprintf("%s:%s", ts.channel, ts.chatID),
					Content:  content,
				})
			}

			toolStart := time.Now()
			var toolResult *tools.ToolResult
			if !allowedTools.Allows(toolName) {
				al.emitEvent(
					EventKindToolExecSkipped,
					ts.eventMeta("runTurn", "turn.tool.skipped"),
					ToolExecSkippedPayload{
						Tool:   toolName,
						Reason: "tool not allowed in this context",
					},
				)
				toolResult = tools.ErrorResult(fmt.Sprintf("tool %q is not available in this context", toolName))
			} else {
				toolExecCtx := tools.WithToolSender(
					tools.WithToolReplyToMessageID(
						tools.WithToolLanguage(
							tools.WithToolPeerKind(turnCtx, ts.opts.PeerKind),
							ts.opts.Language,
						),
						ts.opts.ReplyToMessageID,
					),
					ts.opts.Sender,
				)
				toolExecCtx = tools.WithRequestScope(toolExecCtx, ts.fileScope(turnCtx))
				toolResult = ts.agent.Tools.ExecuteWithContext(
					toolExecCtx,
					toolName,
					toolArgs,
					ts.channel,
					ts.chatID,
					asyncCallback,
				)
			}
			toolDuration := time.Since(toolStart)

			if ts.hardAbortRequested() {
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}

			if al.hooks != nil {
				toolResp, decision := al.hooks.AfterTool(turnCtx, &ToolResultHookResponse{
					Meta:      ts.eventMeta("runTurn", "turn.tool.after"),
					Tool:      toolName,
					Arguments: toolArgs,
					Result:    toolResult,
					Duration:  toolDuration,
					Channel:   ts.channel,
					ChatID:    ts.chatID,
				})
				switch decision.normalizedAction() {
				case HookActionContinue, HookActionModify:
					if toolResp != nil {
						if toolResp.Tool != "" {
							toolName = toolResp.Tool
						}
						if toolResp.Result != nil {
							toolResult = toolResp.Result
						}
					}
				case HookActionAbortTurn:
					turnStatus = TurnEndStatusError
					return turnResult{}, al.hookAbortError(ts, "after_tool", decision)
				case HookActionHardAbort:
					_ = ts.requestHardAbort()
					turnStatus = TurnEndStatusAborted
					return al.abortTurn(ts)
				}
			}

			if toolResult == nil {
				toolResult = tools.ErrorResult("hook returned nil tool result")
			}

			toolResult = al.normalizeToolResult(toolResult)
			if progressEnabled && !toolResult.Async {
				al.publishToolProgress(turnCtx, ts, actionID, toolName, toolArgs, toolResult)
			}
			if !progressEnabled && !toolResult.Terminal && !toolResult.Silent && toolResult.ForUser != "" && ts.opts.SendResponse {
				al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: ts.channel,
					ChatID:  ts.chatID,
					Content: toolResult.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]any{
						"tool":        toolName,
						"content_len": len(toolResult.ForUser),
					})
			}

			if len(toolResult.Media) > 0 {
				parts := make([]bus.MediaPart, 0, len(toolResult.Media))
				for _, ref := range toolResult.Media {
					part := bus.MediaPart{Ref: ref}
					if al.mediaStore != nil {
						if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
							part.Filename = meta.Filename
							part.ContentType = meta.ContentType
							part.Type = inferMediaType(meta.Filename, meta.ContentType)
						}
					}
					parts = append(parts, part)
				}
				al.bus.PublishOutboundMedia(ctx, bus.OutboundMediaMessage{
					Channel: ts.channel,
					ChatID:  ts.chatID,
					Parts:   parts,
				})
			}

			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}
			if strings.TrimSpace(contentForLLM) != "" {
				lastToolResultForFallback = contentForLLM
			}

			// Filter sensitive data (API keys, tokens, secrets) before sending to LLM
			if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
				contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: toolCallID,
			}
			al.emitEvent(
				EventKindToolExecEnd,
				ts.eventMeta("runTurn", "turn.tool.end"),
				ToolExecEndPayload{
					Tool:       toolName,
					Duration:   toolDuration,
					ForLLMLen:  len(contentForLLM),
					ForUserLen: len(toolResult.ForUser),
					IsError:    toolResult.IsError,
					Async:      toolResult.Async,
				},
			)
			messages = append(messages, toolResultMsg)
			if !ts.opts.NoHistory {
				ts.agent.Sessions.AddFullMessage(ts.sessionKey, toolResultMsg)
				ts.recordPersistedMessage(toolResultMsg)
			}

			if toolResult.Terminal && !toolResult.IsError {
				// Delivered terminal tools remain silent. A public completion notice
				// (such as failed artifact storage after an external action) becomes
				// the final response without asking the model to repeat the action.
				if !toolResult.Silent {
					finalContent = toolResult.ForUser
				}
				remaining := len(normalizedToolCalls) - i - 1
				if remaining > 0 {
					logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools after terminal result",
						map[string]any{
							"agent_id":  ts.agent.ID,
							"completed": i + 1,
							"skipped":   remaining,
							"tool":      toolName,
						})
					for j := i + 1; j < len(normalizedToolCalls); j++ {
						skippedTC := normalizedToolCalls[j]
						al.emitEvent(
							EventKindToolExecSkipped,
							ts.eventMeta("runTurn", "turn.tool.skipped"),
							ToolExecSkippedPayload{
								Tool:   skippedTC.Name,
								Reason: "terminal tool result",
							},
						)
						skippedMsg := providers.Message{
							Role:       "tool",
							Content:    "Skipped due to terminal tool result.",
							ToolCallID: skippedTC.ID,
						}
						messages = append(messages, skippedMsg)
						if !ts.opts.NoHistory {
							ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
							ts.recordPersistedMessage(skippedMsg)
						}
					}
				}
				terminalToolCompleted = true
				break
			}

			if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
				pendingMessages = append(pendingMessages, steerMsgs...)
			}

			skipReason := ""
			skipMessage := ""
			if len(pendingMessages) > 0 {
				skipReason = "queued user steering message"
				skipMessage = "Skipped due to queued user message."
			} else if gracefulPending, _ := ts.gracefulInterruptRequested(); gracefulPending {
				skipReason = "graceful interrupt requested"
				skipMessage = "Skipped due to graceful interrupt."
			}

			if skipReason != "" {
				remaining := len(normalizedToolCalls) - i - 1
				if remaining > 0 {
					logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools",
						map[string]any{
							"agent_id":  ts.agent.ID,
							"completed": i + 1,
							"skipped":   remaining,
							"reason":    skipReason,
						})
					for j := i + 1; j < len(normalizedToolCalls); j++ {
						skippedTC := normalizedToolCalls[j]
						al.emitEvent(
							EventKindToolExecSkipped,
							ts.eventMeta("runTurn", "turn.tool.skipped"),
							ToolExecSkippedPayload{
								Tool:   skippedTC.Name,
								Reason: skipReason,
							},
						)
						skippedMsg := providers.Message{
							Role:       "tool",
							Content:    skipMessage,
							ToolCallID: skippedTC.ID,
						}
						messages = append(messages, skippedMsg)
						if !ts.opts.NoHistory {
							ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
							ts.recordPersistedMessage(skippedMsg)
						}
					}
				}
				break
			}

			// Also poll for any SubTurn results that arrived during tool execution.
			if ts.pendingResults != nil {
				select {
				case result, ok := <-ts.pendingResults:
					if ok && result != nil && result.ForLLM != "" {
						content := al.cfg.FilterSensitiveData(result.ForLLM)
						msg := providers.Message{Role: "user", Content: fmt.Sprintf("[SubTurn Result] %s", content)}
						messages = append(messages, msg)
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, msg)
					}
				default:
					// No results available
				}
			}
		}

		ts.agent.Tools.TickTTL()
		logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
			"agent_id": ts.agent.ID, "iteration": iteration,
		})
		if terminalToolCompleted {
			break turnLoop
		}
	}

	if !terminalToolCompleted {
		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			logger.InfoCF("agent", "Steering arrived after turn completion; continuing turn before finalizing",
				map[string]any{
					"agent_id":       ts.agent.ID,
					"steering_count": len(steerMsgs),
					"session_key":    ts.sessionKey,
				})
			pendingMessages = append(pendingMessages, steerMsgs...)
			finalContent = ""
			finalModelContent = ""
			goto turnLoop
		}
	}

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	if finalContent == "" && !terminalToolCompleted {
		if ts.currentIteration() >= ts.agent.MaxIterations && ts.agent.MaxIterations > 0 {
			finalContent = toolExhaustionFallback(ts.opts.Language, lastToolResultForFallback, hadToolCalls)
		} else {
			finalContent = ts.opts.DefaultResponse
		}
	}

	// Delivery quotes belong to the publication, not to canonical model history.
	assistantContent := finalContent
	if finalModelContent != "" {
		assistantContent = finalModelContent
	}
	ts.setPhase(TurnPhaseFinalizing)
	ts.setFinalContent(finalContent)
	if !ts.opts.NoHistory {
		deliveredSaved := recordDeliveredAssistantMessages(
			ctx,
			ts.agent,
			ts.sessionKey,
			ts.channel,
			ts.chatID,
			ts.opts.PeerKind,
			ts.opts.ChatLabel,
			ts.agent.Tools.DeliveredInRound(),
		)
		if !deliveredSaved {
			finalMsg := providers.Message{
				Role:         "assistant",
				Content:      assistantContent,
				ContentPhase: finalContentPhase,
				ModelName:    lastResponseModel,
			}
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, finalMsg)
			ts.recordPersistedMessage(finalMsg)
			recordMemoryObservation(
				ctx,
				ts.agent,
				ts.sessionKey,
				ts.channel,
				ts.chatID,
				ts.opts.PeerKind,
				ts.opts.ChatLabel,
				"assistant",
				"",
				assistantContent,
			)
		}
		if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
			turnStatus = TurnEndStatusError
			al.emitEvent(
				EventKindError,
				ts.eventMeta("runTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_save",
					Message: al.safeErrorText(err),
				},
			)
			return turnResult{}, err
		}
	}

	if !ts.opts.NoHistory && ts.agent.ContextManager != nil {
		if err := ts.agent.ContextManager.Ingest(turnCtx, snapshotContext(ts.agent, ts.sessionKey)); err != nil {
			return turnResult{}, err
		}
	}

	if ts.opts.EnableSummary {
		// The final answer is persisted after the last provider request. Include
		// it once so the threshold represents the next request's full context.
		al.maybeSummarize(
			ctx,
			ts.agent,
			ts.sessionKey,
			ts.scope,
			lastRequestTokens+estimateMessageTokens(providers.Message{
				Role: "assistant", Content: assistantContent,
			}),
		)
	}

	ts.setPhase(TurnPhaseCompleted)
	return turnResult{
		finalContent: finalContent,
		status:       turnStatus,
		followUps:    append([]bus.InboundMessage(nil), ts.followUps...),
	}, nil
}

func (al *AgentLoop) abortTurn(ts *turnState) (turnResult, error) {
	ts.setPhase(TurnPhaseAborted)
	if !ts.opts.NoHistory {
		if err := ts.restoreSession(ts.agent); err != nil {
			al.emitEvent(
				EventKindError,
				ts.eventMeta("abortTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_restore",
					Message: al.safeErrorText(err),
				},
			)
			return turnResult{}, err
		}
	}
	return turnResult{status: TurnEndStatusAborted}, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn. When model routing is configured and the incoming
// message scores below the complexity threshold, it returns the light model
// candidates instead of the primary ones.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn — tool follow-up iterations use the same tier as the initial call so
// that a multi-step tool chain doesn't switch models mid-way.
func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	userMsg string,
	history []providers.Message,
) (provider providers.LLMProvider, candidates []providers.FallbackCandidate, model string) {
	if messageHasRichMediaInput(history) && agent.ImageProvider != nil && len(agent.ImageCandidates) > 0 {
		logger.InfoCF("agent", "Model routing: rich media model selected",
			map[string]any{
				"agent_id":    agent.ID,
				"image_model": agent.ImageModel,
			})
		return agent.ImageProvider, agent.ImageCandidates, resolvedCandidateModel(
			agent.ImageCandidates,
			agent.ImageModel,
		)
	}

	if agent.Router == nil || len(agent.LightCandidates) == 0 {
		return agent.Provider, agent.Candidates, resolvedCandidateModel(agent.Candidates, agent.Model)
	}

	_, usedLight, score := agent.Router.SelectModel(userMsg, history, agent.Model)
	if !usedLight {
		logger.DebugCF("agent", "Model routing: primary model selected",
			map[string]any{
				"agent_id":  agent.ID,
				"score":     score,
				"threshold": agent.Router.Threshold(),
			})
		return agent.Provider, agent.Candidates, resolvedCandidateModel(agent.Candidates, agent.Model)
	}

	logger.InfoCF("agent", "Model routing: light model selected",
		map[string]any{
			"agent_id":    agent.ID,
			"light_model": agent.Router.LightModel(),
			"score":       score,
			"threshold":   agent.Router.Threshold(),
		})
	return agent.Provider, agent.LightCandidates, resolvedCandidateModel(
		agent.LightCandidates,
		agent.Router.LightModel(),
	)
}

func messageHasRichMediaInput(messages []providers.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		for _, mediaRef := range msg.Media {
			if strings.HasPrefix(mediaRef, "data:image/") {
				return true
			}
		}
		return false
	}
	return false
}

func sameTargetReplyToMessageID(ctx context.Context, channel, chatID string) string {
	if strings.TrimSpace(channel) != strings.TrimSpace(tools.ToolChannel(ctx)) {
		return ""
	}
	if strings.TrimSpace(chatID) != strings.TrimSpace(tools.ToolChatID(ctx)) {
		return ""
	}
	return tools.ToolReplyToMessageID(ctx)
}

func publishToolMessage(msgBus *bus.MessageBus, toolCtx context.Context, channel, chatID, content string) error {
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	return msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel:          channel,
		ChatID:           chatID,
		Content:          content,
		ReplyToMessageID: sameTargetReplyToMessageID(toolCtx, channel, chatID),
	})
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	turnScope turnEventScope,
	requestTokens int,
) {
	// Observe-only messages do not create a turn; bind their background
	// summarization too, so a late summary cannot cross a reset boundary.
	if store, ok := agent.Sessions.(*session.ConversationStore); ok {
		turnStore, _, err := store.CaptureTurn(sessionKey)
		if err != nil {
			logger.WarnCF(
				"agent",
				"Cannot capture conversation for summarization",
				map[string]any{"error": al.safeErrorText(err)},
			)
			return
		}
		turnAgent := *agent
		turnAgent.Sessions = turnStore
		agent = &turnAgent
	}
	if manager, ok := agent.ContextManager.(*seahorseContextManager); ok {
		s := snapshotContext(agent, sessionKey)
		if requestTokens > agent.ContextWindow*agent.SummarizeTokenPercent/100 {
			assembled, err := manager.Assemble(ctx, s)
			if err != nil {
				logger.WarnCF(
					"agent",
					"Cannot prepare context compaction",
					map[string]any{"error": al.safeErrorText(err)},
				)
				return
			}
			contributors := max(0, requestTokens-estimateTokens(assembled.History))
			manager.scheduleCompact(
				ctx,
				s,
				max(1, agent.ContextWindow*agent.SummarizeTokenPercent/100-contributors-512),
			)
		}
		return
	}

	newHistory := agent.Sessions.GetHistory(sessionKey)
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100

	if len(newHistory) > agent.SummarizeMessageThreshold || requestTokens > threshold {
		if manager, ok := agent.ContextManager.(*legacyContextManager); ok {
			summaryAgent := *agent
			if agent.ContextProvider != nil {
				summaryAgent.Provider = agent.ContextProvider
			}
			summaryAgent.Model = resolvedCandidateModel(agent.Candidates, agent.Model)
			manager.scheduleSummary(ctx, snapshotContext(agent, sessionKey), func(workCtx context.Context) {
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				al.summarizeSession(workCtx, &summaryAgent, sessionKey, turnScope)
			})
		}
	}
}

type compressionResult struct {
	DroppedMessages   int
	RemainingMessages int
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest ~50% of Turns (a Turn is a complete user→LLM→response
// cycle, as defined in #1316), so tool-call sequences are never split.
//
// protectedMessages preserves the active turn during a provider retry. If the
// history is a single Turn with no safe split point and no active turn, the function
// falls back to keeping only the most recent user message. This breaks
// Turn atomicity as a last resort to avoid a context-exceeded loop.
//
// Session history contains only user/assistant/tool messages — the system
// prompt is built dynamically by BuildMessages and is NOT stored here.
// The compression note is recorded in the session summary so that
// BuildMessages can include it in the next system prompt.
func (al *AgentLoop) forceCompression(
	agent *AgentInstance,
	sessionKey string,
	protectedMessages int,
) (compressionResult, bool) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 2 {
		return compressionResult{}, false
	}

	// Split at a Turn boundary so no tool-call sequence is torn apart.
	// parseTurnBoundaries gives us the start of each Turn; we drop the
	// oldest half of Turns and keep the most recent ones.
	turns := parseTurnBoundaries(history)
	var mid int
	if len(turns) >= 2 {
		mid = turns[len(turns)/2]
	} else {
		// Fewer than 2 Turns — fall back to message-level midpoint
		// aligned to the nearest Turn boundary.
		mid = findSafeBoundary(history, len(history)/2)
	}
	if protectedMessages > 0 {
		mid = min(mid, len(history)-protectedMessages)
		if mid <= 0 {
			return compressionResult{}, false
		}
	}
	var keptHistory []providers.Message
	if mid <= 0 {
		// No safe Turn boundary — the entire history is a single Turn
		// (e.g. one user message followed by a massive tool response).
		// Keeping everything would leave the agent stuck in a context-
		// exceeded loop, so fall back to keeping only the most recent
		// user message. This breaks Turn atomicity as a last resort.
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				keptHistory = []providers.Message{history[i]}
				break
			}
		}
	} else {
		keptHistory = history[mid:]
	}

	droppedCount := len(history) - len(keptHistory)

	// Record compression in the session summary so BuildMessages includes it
	// in the system prompt. We do not modify history messages themselves.
	existingSummary := agent.Sessions.GetSummary(sessionKey)
	compressionNote := fmt.Sprintf(
		"[Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	if existingSummary != "" {
		compressionNote = existingSummary + "\n\n" + compressionNote
	}
	agent.Sessions.SetSummary(sessionKey, compressionNote)

	agent.Sessions.SetHistory(sessionKey, keptHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(keptHistory),
	})

	return compressionResult{
		DroppedMessages:   droppedCount,
		RemainingMessages: len(keptHistory),
	}, true
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	registry := al.GetRegistry()
	agent := registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(registry.ListAgentIDs()),
		"ids":   registry.ListAgentIDs(),
	}

	return info
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(
						&sb,
						"      Arguments: %s\n",
						utils.Truncate(tc.Function.Arguments, 200),
					)
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(
				&sb,
				"      Parameters: %s\n",
				utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200),
			)
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	turnScope turnEventScope,
) {
	if ctx.Err() != nil {
		return
	}

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	// Keep the most recent Turns for continuity, aligned to a Turn boundary
	// so that no tool-call sequence is split.
	if len(history) <= 4 {
		return
	}

	safeCut := findSafeBoundary(history, len(history)-4)
	if safeCut <= 0 {
		return
	}
	keepCount := len(history) - safeCut
	toSummarize := history[:safeCut]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	const (
		maxSummarizationMessages = 10
		llmMaxRetries            = 3
		llmTemperature           = 0.3
		fallbackMaxContentLength = 200
	)

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > maxSummarizationMessages {
		mid := len(validMessages) / 2

		mid = al.findNearestUserMessage(validMessages, mid)

		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)

		resp, err := al.retryLLMCall(ctx, agent, mergePrompt, llmMaxRetries)
		if err == nil && resp.Content != "" {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if ctx.Err() != nil {
		return
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, keepCount)
		agent.Sessions.Save(sessionKey)
		al.emitEvent(
			EventKindSessionSummarize,
			turnScope.meta(0, "summarizeSession", "turn.session.summarize"),
			SessionSummarizePayload{
				SummarizedMessages: len(validMessages),
				KeptMessages:       keepCount,
				SummaryLen:         len(finalSummary),
				OmittedOversized:   omitted,
			},
		)
	}
}

// findNearestUserMessage finds the nearest user message to the given index.
// It searches backward first, then forward if no user message is found.
func (al *AgentLoop) findNearestUserMessage(messages []providers.Message, mid int) int {
	originalMid := mid

	for mid > 0 && messages[mid].Role != "user" {
		mid--
	}

	if messages[mid].Role == "user" {
		return mid
	}

	mid = originalMid
	for mid < len(messages) && messages[mid].Role != "user" {
		mid++
	}

	if mid < len(messages) {
		return mid
	}

	return originalMid
}

// retryLLMCall calls the LLM with retry logic.
func (al *AgentLoop) retryLLMCall(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxRetries int,
) (*providers.LLMResponse, error) {
	const (
		llmTemperature = 0.3
	)

	var resp *providers.LLMResponse
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if canceled := ctx.Err(); canceled != nil {
			return resp, canceled
		}
		al.activeRequests.Add(1)
		resp, err = func() (*providers.LLMResponse, error) {
			defer al.activeRequests.Done()
			return chatContextModel(
				ctx, agent.Provider, contextInactivityTimeout(agent),
				[]providers.Message{{Role: "user", Content: prompt}},
				agent.Model,
				map[string]any{
					"max_tokens":       agent.MaxTokens,
					"temperature":      llmTemperature,
					"prompt_cache_key": agent.ID,
				},
			)
		}()
		if canceled := ctx.Err(); canceled != nil {
			return resp, canceled
		}

		if err == nil && resp != nil && resp.Content != "" {
			return resp, nil
		}
		if attempt < maxRetries-1 {
			if waitErr := sleepWithContext(ctx, time.Duration(attempt+1)*100*time.Millisecond); waitErr != nil {
				return resp, waitErr
			}
		}
	}

	return resp, err
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	const (
		llmMaxRetries             = 3
		llmTemperature            = 0.3
		fallbackMinContentLength  = 200
		fallbackMaxContentPercent = 10
	)

	var sb strings.Builder
	sb.WriteString(
		"Provide a concise summary of this conversation segment, preserving core context and key points.\n",
	)
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := al.retryLLMCall(ctx, agent, prompt, llmMaxRetries)
	if err == nil && response.Content != "" {
		return strings.TrimSpace(response.Content), nil
	}

	var fallback strings.Builder
	fallback.WriteString("Conversation summary: ")
	for i, m := range batch {
		if i > 0 {
			fallback.WriteString(" | ")
		}
		content := strings.TrimSpace(m.Content)
		runes := []rune(content)
		if len(runes) == 0 {
			fallback.WriteString(fmt.Sprintf("%s: ", m.Role))
			continue
		}

		keepLength := len(runes) * fallbackMaxContentPercent / 100
		if keepLength < fallbackMinContentLength {
			keepLength = fallbackMinContentLength
		}

		if keepLength > len(runes) {
			keepLength = len(runes)
		}

		content = string(runes[:keepLength])
		if keepLength < len(runes) {
			content += "..."
		}
		fallback.WriteString(fmt.Sprintf("%s: %s", m.Role, content))
	}
	return fallback.String(), nil
}

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	agent *AgentInstance,
	opts *processOptions,
) (string, bool) {
	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if al.cmdRegistry == nil {
		return "", false
	}

	rt := al.buildCommandsRuntime(agent, opts)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: msg.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default: // OutcomePassthrough — let the message fall through to LLM
		return "", false
	}
}

func (al *AgentLoop) buildCommandsRuntime(agent *AgentInstance, opts *processOptions) *commands.Runtime {
	registry := al.GetRegistry()
	cfg := al.GetConfig()
	rt := &commands.Runtime{
		Config:          cfg,
		ListAgentIDs:    registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
		GetEnabledChannels: func() []string {
			if al.channelManager == nil {
				return nil
			}
			return al.channelManager.GetEnabledChannels()
		},
		GetActiveTurn: func() any {
			info := al.GetActiveTurn()
			if info == nil {
				return nil
			}
			return info
		},
		SwitchChannel: func(value string) error {
			if al.channelManager == nil {
				return fmt.Errorf("channel manager not initialized")
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Errorf("channel '%s' not found or not enabled", value)
			}
			return nil
		},
	}
	rt.ReloadConfig = func() error {
		if al.reloadFunc == nil {
			return fmt.Errorf("reload not configured")
		}
		return al.reloadFunc()
	}
	if agent != nil {
		rt.GetModelInfo = func() (string, string) {
			if al.hasSavedRouting() {
				return al.savedModelInfo(agent)
			}
			return agent.Model, resolvedCandidateProvider(agent.Candidates, cfg.Agents.Defaults.Provider)
		}
		rt.SwitchModel = func(value string) (string, error) {
			value = strings.TrimSpace(value)
			if al.hasSavedRouting() {
				return al.switchSavedModel(agent, value)
			}
			modelCfg, err := resolvedModelConfig(cfg, value, agent.Workspace)
			if err != nil {
				return "", err
			}

			nextProvider, _, err := providers.CreateProviderFromConfig(modelCfg)
			if err != nil {
				return "", fmt.Errorf("failed to initialize model %q: %w", value, err)
			}

			nextCandidates := resolveModelCandidates(
				cfg,
				cfg.Agents.Defaults.Provider,
				value,
				agent.Fallbacks,
				nextProvider,
			)
			if len(nextCandidates) == 0 {
				return "", fmt.Errorf("model %q did not resolve to any provider candidates", value)
			}

			oldModel := agent.Model
			oldProvider := agent.Provider
			agent.Model = value
			agent.Provider = nextProvider
			agent.ContextProvider = nextProvider
			agent.Candidates = nextCandidates
			agent.ThinkingLevel = parseThinkingLevel(modelCfg.ThinkingLevel)

			if oldProvider != nil && oldProvider != nextProvider {
				if manager, ok := agent.ContextManager.(contextProviderOwner); ok {
					manager.retainProvider(oldProvider)
					manager.retainProvider(nextProvider)
				} else if stateful, ok := oldProvider.(providers.StatefulProvider); ok {
					stateful.Close()
				}
			}
			return oldModel, nil
		}

		rt.ClearHistory = func() error {
			if opts == nil {
				return fmt.Errorf("process options not available")
			}
			if agent.Sessions == nil {
				return fmt.Errorf("sessions not initialized for agent")
			}

			return al.resetConversation(agent, opts.SessionKey)
		}
		if opts != nil && al.cfg.IsChannelType(opts.Channel, "telegram") && opts.PeerKind == "direct" &&
			opts.TelegramUserID != "" {
			rt.ClearInlineHistory = func() error {
				return al.resetConversation(
					agent,
					inlineConversationKeyForChannel(agent.ID, opts.TelegramUserID, opts.Channel),
				)
			}
		}
	}
	return rt
}

func mapCommandError(result commands.ExecuteResult) string {
	if result.Command == "" {
		return fmt.Sprintf("Failed to execute command: %v", result.Err)
	}
	return fmt.Sprintf("Failed to execute /%s: %v", result.Command, result.Err)
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

func inboundMetadata(msg bus.InboundMessage, key string) string {
	if msg.Metadata == nil {
		return ""
	}
	return msg.Metadata[key]
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}

// isNativeSearchProvider reports whether the given LLM provider implements
// NativeSearchCapable and returns true for SupportsNativeSearch.
func isNativeSearchProvider(p providers.LLMProvider) bool {
	if ns, ok := p.(providers.NativeSearchCapable); ok {
		return ns.SupportsNativeSearch()
	}
	return false
}

// filterClientWebSearch returns a copy of tools with the client-side
// web_search tool removed. Used when native provider search is preferred.
func filterClientWebSearch(tools []providers.ToolDefinition) []providers.ToolDefinition {
	result := make([]providers.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if strings.EqualFold(t.Function.Name, "web_search") {
			continue
		}
		result = append(result, t)
	}
	return result
}

// Helper to extract provider from registry for cleanup
func extractProvider(registry *AgentRegistry) (providers.LLMProvider, bool) {
	if registry == nil {
		return nil, false
	}
	// Get any agent to access the provider
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, false
	}
	return defaultAgent.Provider, true
}
