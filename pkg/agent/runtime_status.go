package agent

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/modelruntime"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// EnableRuntimeStatus initializes an instance-specific artifact before listeners start.
func (al *AgentLoop) EnableRuntimeStatus(configPath string) (*modelruntime.Collector, error) {
	collector, err := modelruntime.New(configPath, al.runtimeAvailability)
	if err != nil {
		return nil, err
	}
	al.mu.Lock()
	al.runtimeStatus = collector
	al.mu.Unlock()
	return collector, nil
}

// runtimeAvailability reports the saved models' routing availability and the
// Key pool key states that only this process holds, for the dashboard.
func (al *AgentLoop) runtimeAvailability() ([]providers.RoutingAvailability, []modelruntime.KeyPoolStatus) {
	al.mu.RLock()
	source, cfg, chain := al.routingSource, al.cfg, al.fallback
	al.mu.RUnlock()
	if source != nil {
		loaded, err := source()
		if err != nil {
			return nil, nil
		}
		cfg = loaded
	}
	if cfg == nil {
		return nil, nil
	}
	pools := make([]modelruntime.KeyPoolStatus, 0, len(cfg.KeyPools))
	for i := range cfg.KeyPools {
		pool := &cfg.KeyPools[i]
		pools = append(pools, modelruntime.KeyPoolStatus{ID: pool.ID, Keys: providers.KeyPoolAvailability(pool)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := []providers.RoutingAvailability{}
	for _, model := range cfg.ModelList[:min(len(cfg.ModelList), modelruntime.MaxCandidates)] {
		item, err := chain.Availability(ctx, candidateFromConfig(&model))
		if err != nil {
			return nil, pools
		}
		result = append(result, item)
	}
	return result, pools
}

func routingIdentities(candidates []providers.FallbackCandidate) []providers.RoutingIdentity {
	result := make([]providers.RoutingIdentity, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.RoutingIdentity())
	}
	return result
}

func (al *AgentLoop) beginRuntimeTurn(ctx context.Context, ts *turnState) context.Context {
	al.mu.RLock()
	collector := al.runtimeStatus
	al.mu.RUnlock()
	if collector == nil {
		return ctx
	}
	kind, parent := "request", ""
	if ts.parentTurnState != nil {
		kind = "subturn"
		if trace := ts.parentTurnState.runtimeTrace; trace != nil {
			parent = trace.ID
		}
	}
	trace := collector.Begin(ts.agentID, kind, parent, routingIdentities(ts.agent.Candidates))
	ts.runtimeTrace = trace
	// Context snapshots retain a separate immutable parent link. The concrete
	// adapter type must remain intact for per-candidate inactivity ownership.
	if adapter, ok := ts.agent.ContextProvider.(*routingContextProvider); ok {
		bound := *adapter
		bound.runtimeParent = trace
		ts.agent.ContextProvider = &bound
		if ts.agent.Provider == adapter {
			ts.agent.Provider = &bound
		}
		if ts.agent.ImageProvider == adapter {
			ts.agent.ImageProvider = &bound
		}
	}
	return providers.WithRoutingObserver(ctx, trace.Record)
}
