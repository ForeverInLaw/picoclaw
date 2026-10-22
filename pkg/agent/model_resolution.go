package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func resolveModelCandidates(
	cfg *config.Config,
	defaultProvider string,
	primary string,
	fallbacks []string,
	selected ...providers.LLMProvider,
) []providers.FallbackCandidate {
	var candidates []providers.FallbackCandidate
	seen := make(map[string]bool)
	add := func(raw, selectedKey string) *config.ModelConfig {
		var mc *config.ModelConfig
		if cfg != nil && selectedKey != "" {
			for i := range cfg.ModelList {
				_, id := providers.ExtractProtocol(cfg.ModelList[i].Model)
				matches := cfg.ModelList[i].ModelName == raw || cfg.ModelList[i].Model == raw || id == raw
				if matches && providers.ModelConfigKey(&cfg.ModelList[i]) == selectedKey {
					clone := cfg.ModelList[i]
					mc = &clone
					break
				}
			}
		}
		if mc == nil {
			mc, _ = resolvedModelConfig(cfg, raw, "")
		}
		var candidate providers.FallbackCandidate
		if mc != nil {
			candidate = candidateFromConfig(mc)
		} else if ref := providers.ParseModelRef(raw, defaultProvider); ref != nil && !strings.HasPrefix(raw, config.ModelIDPrefix) {
			candidate = providers.FallbackCandidate{Provider: ref.Provider, Model: ref.Model}
		}
		if candidate.Model != "" && !seen[candidate.StableKey()] {
			seen[candidate.StableKey()] = true
			candidates = append(candidates, candidate)
		}
		return mc
	}
	selectedKey := ""
	if len(selected) > 0 {
		if configured, ok := selected[0].(interface{ ModelConfigKey() string }); ok {
			selectedKey = configured.ModelConfigKey()
		}
	}
	primaryConfig := add(primary, selectedKey)
	if fallbacks == nil && primaryConfig != nil {
		fallbacks = primaryConfig.Fallbacks
	}
	for _, raw := range fallbacks {
		add(raw, "")
	}
	return candidates
}

func candidateFromConfig(mc *config.ModelConfig) providers.FallbackCandidate {
	ref := providers.ParseModelRef(mc.Model, "openai")
	if ref == nil {
		return providers.FallbackCandidate{}
	}
	return providers.FallbackCandidate{
		Provider:   ref.Provider,
		Model:      ref.Model,
		ConfigName: mc.ModelName,
		ConfigKey:  providers.ModelConfigKey(mc),
		ConfigID:   mc.ID,
		AccountID:  mc.AccountID,
	}
}

func resolvedCandidateModel(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Model) != "" {
		return candidates[0].Model
	}
	return fallback
}

func resolvedCandidateProvider(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Provider) != "" {
		return candidates[0].Provider
	}
	return fallback
}

func resolvedModelConfig(cfg *config.Config, modelName, workspace string) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	modelName = strings.TrimSpace(modelName)
	modelCfg, err := cfg.GetModelConfig(modelName)
	if err != nil {
		matching := *cfg
		matching.ModelList = nil
		for _, candidate := range cfg.ModelList {
			_, id := providers.ExtractProtocol(candidate.Model)
			if candidate.Model == modelName || id == modelName {
				matching.ModelList = append(matching.ModelList, candidate)
			}
		}
		if len(matching.ModelList) > 0 {
			modelCfg, err = matching.GetModelConfig(matching.ModelList[0].ModelName)
		}
		if err != nil {
			return nil, err
		}
	}

	clone := *modelCfg
	if clone.Workspace == "" {
		clone.Workspace = workspace
	}

	return &clone, nil
}

// modelConfigForCandidate never borrows another alias's credentials after
// configuration identity was selected.
func modelConfigForCandidate(
	cfg *config.Config,
	candidate providers.FallbackCandidate,
	workspace string,
) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if candidate.ConfigKey != "" {
		for _, mc := range cfg.ModelList {
			if providers.ModelConfigKey(&mc) == candidate.ConfigKey {
				if mc.Workspace == "" {
					mc.Workspace = workspace
				}
				return &mc, nil
			}
		}
		return nil, fmt.Errorf("selected model configuration %q is no longer available", candidate.ConfigName)
	}
	if candidate.ConfigName != "" {
		return resolvedModelConfig(cfg, candidate.ConfigName, workspace)
	}
	matching := *cfg
	matching.ModelList = nil
	for _, mc := range cfg.ModelList {
		protocol, id := providers.ExtractProtocol(mc.Model)
		if providers.NormalizeProvider(protocol) == providers.NormalizeProvider(candidate.Provider) &&
			id == candidate.Model {
			matching.ModelList = append(matching.ModelList, mc)
		}
	}
	if len(matching.ModelList) == 0 {
		return nil, fmt.Errorf("no model configuration for fallback %s/%s", candidate.Provider, candidate.Model)
	}
	return resolvedModelConfig(&matching, matching.ModelList[0].ModelName, workspace)
}

// candidateProviderResolver keeps one exact endpoint/key selection per turn.
func candidateProviderResolver(
	cfg *config.Config,
	workspace string,
	primary providers.LLMProvider,
	candidates []providers.FallbackCandidate,
) func(providers.FallbackCandidate) (providers.LLMProvider, error) {
	clients := make(map[string]providers.LLMProvider)
	var clientsMu sync.Mutex
	primaryProvider := ""
	if len(candidates) > 0 {
		primaryProvider = candidates[0].Provider
		if configured, ok := primary.(interface{ ModelConfigKey() string }); !ok || configured.ModelConfigKey() == "" ||
			configured.ModelConfigKey() == candidates[0].ConfigKey {
			clients[candidates[0].StableKey()] = primary
		}
	}
	return func(candidate providers.FallbackCandidate) (providers.LLMProvider, error) {
		key := candidate.StableKey()
		clientsMu.Lock()
		cached := clients[key]
		clientsMu.Unlock()
		if cached != nil {
			return cached, nil
		}
		mc, err := modelConfigForCandidate(cfg, candidate, workspace)
		if err != nil {
			if candidate.ConfigKey == "" && candidate.ConfigName == "" && candidate.Provider == primaryProvider {
				return primary, nil
			}
			return nil, err
		}
		client, _, err := providers.CreateProviderFromConfig(mc)
		if err != nil {
			return nil, err
		}
		// Stateful clients created for a candidate belong to its synchronous call.
		// The complete canonical request carries continuity into the next call.
		if _, stateful := client.(providers.StatefulProvider); !stateful {
			clientsMu.Lock()
			clients[key] = client
			clientsMu.Unlock()
		}
		return client, nil
	}
}

// ConfiguredDefaultModels uses the same exact-entry resolution as user requests.
// Startup readiness may inspect this chain without selecting a replacement default.
func ConfiguredDefaultModels(cfg *config.Config) []config.ModelConfig {
	if cfg == nil {
		return nil
	}
	candidates := resolveModelCandidates(
		cfg,
		cfg.Agents.Defaults.Provider,
		cfg.Agents.Defaults.GetModelName(),
		cfg.Agents.Defaults.ModelFallbacks,
	)
	models := make([]config.ModelConfig, 0, len(candidates))
	for _, candidate := range candidates {
		if mc, err := modelConfigForCandidate(cfg, candidate, ""); err == nil {
			models = append(models, *mc)
		}
	}
	return models
}
