package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type temporaryModelSelection struct {
	modelID    string
	savedChain string
}

func agentModelSettings(cfg *config.Config, id string) *config.AgentConfig {
	for i := range cfg.Agents.List {
		if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == id {
			return &cfg.Agents.List[i]
		}
	}
	return nil
}

// savedChainRevision identifies routing policy, independently of credentials,
// endpoint, label and model-list order. Duplicate legacy aliases keep all their
// possible configured identities without advancing round-robin selection.
func savedChainRevision(cfg *config.Config, id string) string {
	settings := agentModelSettings(cfg, id)
	primary := resolveAgentModel(settings, &cfg.Agents.Defaults)
	fallbacks := resolveAgentFallbacks(settings, &cfg.Agents.Defaults)
	matches := func(reference string) []config.ModelConfig {
		models := []config.ModelConfig{}
		byID := strings.HasPrefix(reference, config.ModelIDPrefix)
		for _, model := range cfg.ModelList {
			if byID && model.ID == strings.TrimPrefix(reference, config.ModelIDPrefix) ||
				!byID && model.ModelName == reference {
				models = append(models, model)
			}
		}
		if len(models) > 0 || byID {
			return models
		}
		for _, model := range cfg.ModelList {
			_, slug := providers.ExtractProtocol(model.Model)
			if model.Model == reference || slug == reference {
				models = append(models, model)
			}
		}
		return models
	}
	normalize := func(reference string) string {
		models := matches(reference)
		identities := make([]string, 0, len(models))
		for _, model := range models {
			identities = append(identities, config.ModelIDPrefix+model.ID)
		}
		if len(identities) == 0 {
			return reference
		}
		sort.Strings(identities)
		encoded, _ := json.Marshal(identities)
		return string(encoded)
	}
	primaries := matches(primary)
	if len(primaries) == 0 {
		primaries = []config.ModelConfig{{ID: primary}}
	}
	policies := make([]string, 0, len(primaries))
	for _, model := range primaries {
		reserves := fallbacks
		if reserves == nil {
			reserves = model.Fallbacks
		}
		policy := []string{config.ModelIDPrefix + model.ID}
		for _, reserve := range reserves {
			policy = append(policy, normalize(reserve))
		}
		encoded, _ := json.Marshal(policy)
		policies = append(policies, string(encoded))
	}
	sort.Strings(policies)
	encoded, _ := json.Marshal(policies)
	return string(encoded)
}

func (al *AgentLoop) hasSavedRouting() bool {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.routingSource != nil
}

func (al *AgentLoop) selectedSavedModel(cfg *config.Config, id, primary string) string {
	al.mu.Lock()
	defer al.mu.Unlock()
	selection, ok := al.temporaryModels[id]
	if !ok {
		return primary
	}
	if selection.savedChain != savedChainRevision(cfg, id) {
		delete(al.temporaryModels, id)
		return primary
	}
	reference := config.ModelIDPrefix + selection.modelID
	if _, err := cfg.GetModelConfig(reference); err != nil {
		delete(al.temporaryModels, id)
		return primary
	}
	return reference
}

func (al *AgentLoop) switchSavedModel(agent *AgentInstance, value string) (string, error) {
	captured, err := al.captureRouting(agent)
	if err != nil {
		return "", err
	}
	cfg := captured.requestRouting.cfg
	model, err := resolvedModelConfig(cfg, value, agent.Workspace)
	if err != nil {
		return "", err
	}
	// Preserve command validation without transferring a temporary client into
	// registry/context ownership; subsequent requests resolve captured settings.
	client, _, err := providers.CreateProviderFromConfig(model)
	if err != nil {
		return "", fmt.Errorf("failed to initialize model %q: %w", value, err)
	}
	if stateful, owned := client.(providers.StatefulProvider); owned {
		stateful.Close()
	}
	old := captured.Model
	if len(captured.Candidates) > 0 && captured.Candidates[0].ConfigName != "" {
		old = captured.Candidates[0].ConfigName
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.temporaryModels == nil {
		al.temporaryModels = make(map[string]temporaryModelSelection)
	}
	al.temporaryModels[agent.ID] = temporaryModelSelection{
		modelID:    model.ID,
		savedChain: savedChainRevision(cfg, agent.ID),
	}
	return old, nil
}

func (al *AgentLoop) savedModelInfo(agent *AgentInstance) (string, string) {
	captured, err := al.captureRouting(agent)
	if err != nil {
		return "unavailable", "unknown"
	}
	name := captured.Model
	if len(captured.Candidates) > 0 && captured.Candidates[0].ConfigName != "" {
		name = captured.Candidates[0].ConfigName
	}
	return name, resolvedCandidateProvider(captured.Candidates, captured.requestRouting.cfg.Agents.Defaults.Provider)
}
