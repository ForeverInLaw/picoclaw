package agent

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func contextInactivityTimeout(agent *AgentInstance) time.Duration {
	if agent.requestRouting != nil && len(agent.Candidates) > 0 {
		return candidateInactivityTimeout(agent.requestRouting.cfg, agent.Candidates[0], agent.Workspace)
	}
	return providers.DefaultInactivityTimeout
}

// Routed context providers time each candidate internally, keeping recovery
// pauses outside the model wait. Ordinary providers need one bounded attempt.
func chatContextModel(
	ctx context.Context,
	client providers.LLMProvider,
	limit time.Duration,
	messages []providers.Message,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	call := func(attemptCtx context.Context) (*providers.LLMResponse, error) {
		return client.Chat(attemptCtx, messages, nil, model, options)
	}
	if _, routed := client.(*routingContextProvider); routed {
		return call(ctx)
	}
	return providers.RunModelAttempt(ctx, limit, call)
}
