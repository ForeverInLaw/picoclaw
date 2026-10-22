package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// SetRoutingConfigSource supplies the authoritative saved routing at request
// start. It is independent of gateway watcher and never rebuilds agent resources.
func (al *AgentLoop) SetRoutingConfigSource(source func() (*config.Config, error)) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.routingSource = source
}

type requestRouting struct {
	cfg      *config.Config
	mu       sync.Mutex
	selected providers.FallbackCandidate
}

func (r *requestRouting) prefer(candidates []providers.FallbackCandidate) []providers.FallbackCandidate {
	if r == nil {
		return candidates
	}
	r.mu.Lock()
	selected := r.selected
	r.mu.Unlock()
	for i, c := range candidates {
		if c.StableKey() == selected.StableKey() && i > 0 {
			ordered := append([]providers.FallbackCandidate{c}, candidates[:i]...)
			return append(ordered, candidates[i+1:]...)
		}
	}
	return candidates
}

func (r *requestRouting) served(candidate providers.FallbackCandidate) {
	if r != nil {
		r.mu.Lock()
		r.selected = candidate
		r.mu.Unlock()
	}
}

func (al *AgentLoop) captureRouting(agent *AgentInstance) (*AgentInstance, error) {
	al.mu.RLock()
	source := al.routingSource
	cfg := al.cfg
	al.mu.RUnlock()
	copyAgent := *agent
	if source != nil {
		var err error
		cfg, err = source()
		if err != nil {
			return nil, fmt.Errorf("load request routing: %w", err)
		}
		if cfg == nil {
			return nil, fmt.Errorf("request routing source returned no configuration")
		}
		if err := cfg.ValidateRoutingReferences(); err != nil {
			return nil, err
		}
		var settings *config.AgentConfig
		for i := range cfg.Agents.List {
			if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == agent.ID {
				settings = &cfg.Agents.List[i]
				break
			}
		}
		defaults := &cfg.Agents.Defaults
		copyAgent.Model = resolveAgentModel(settings, defaults)
		copyAgent.Fallbacks = resolveAgentFallbacks(settings, defaults)
		copyAgent.Candidates = resolveModelCandidates(cfg, defaults.Provider, copyAgent.Model, copyAgent.Fallbacks)
		if len(copyAgent.Candidates) == 0 {
			return nil, fmt.Errorf("no configured models available for agent %s", agent.ID)
		}
		copyAgent.ImageModel = defaults.ImageModel
		copyAgent.ImageFallbacks = defaults.ImageModelFallbacks
		copyAgent.ImageCandidates = nil
		copyAgent.ImageProvider = nil
		if defaults.ImageModel != "" {
			copyAgent.ImageCandidates = resolveModelCandidates(
				cfg,
				defaults.Provider,
				defaults.ImageModel,
				defaults.ImageModelFallbacks,
			)
		}
		copyAgent.LightCandidates = nil
		copyAgent.Router = nil
		if rc := defaults.Routing; rc != nil && rc.Enabled && rc.LightModel != "" {
			copyAgent.LightCandidates = resolveModelCandidates(cfg, defaults.Provider, rc.LightModel, nil)
			copyAgent.Router = routing.New(routing.RouterConfig{LightModel: rc.LightModel, Threshold: rc.Threshold})
		}
		if mc, err := modelConfigForCandidate(cfg, copyAgent.Candidates[0], copyAgent.Workspace); err == nil {
			copyAgent.ThinkingLevel = parseThinkingLevel(mc.ThinkingLevel)
		}
	}
	copyAgent.requestRouting = &requestRouting{cfg: cfg}
	if source != nil {
		adapter := &routingContextProvider{
			routing:    copyAgent.requestRouting,
			chain:      al.fallback,
			candidates: copyAgent.Candidates,
			agent:      &copyAgent,
			resolve:    candidateProviderResolver(cfg, copyAgent.Workspace, nil, copyAgent.Candidates),
		}
		copyAgent.Provider = adapter
		copyAgent.ContextProvider = adapter
		if len(copyAgent.ImageCandidates) > 0 {
			copyAgent.ImageProvider = adapter
		}
	}
	return &copyAgent, nil
}

// Context operations use the same candidate resolver and fallback executor as
// foreground inference. The adapter retains the request's model configuration
// when a background summary outlives its originating request.
type routingContextProvider struct {
	mu         sync.Mutex
	routing    *requestRouting
	chain      *providers.FallbackChain
	agent      *AgentInstance
	candidates []providers.FallbackCandidate
	resolve    func(providers.FallbackCandidate) (providers.LLMProvider, error)
}

func (p *routingContextProvider) GetDefaultModel() string {
	return resolvedCandidateModel(p.candidates, p.agent.Model)
}
func (*routingContextProvider) ModelConfigKey() string { return "request-routing-adapter" }

func (p *routingContextProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	result, err := p.chain.ExecuteRecoveryCandidate(
		ctx,
		p.routing.prefer(p.candidates),
		func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
			p.mu.Lock()
			client, err := p.resolve(candidate)
			p.mu.Unlock()
			if err != nil {
				return nil, err
			}
			definitions, opts := prepareCandidateRequest(
				p.routing.cfg,
				client,
				candidate,
				tools,
				options,
				p.agent.ThinkingLevel,
			)
			if err := checkCandidateContext(
				p.routing.cfg,
				candidate,
				client,
				messages,
				definitions,
				opts,
				0,
				p.agent.MaxTokens,
			); err != nil {
				return nil, &providers.IncompatibleCandidateError{Reason: err.Error()}
			}
			return client.Chat(ctx, messages, definitions, candidate.Model, opts)
		},
		nil,
	)
	if err != nil {
		return nil, err
	}
	p.routing.served(result.Candidate)
	return result.Response, nil
}
