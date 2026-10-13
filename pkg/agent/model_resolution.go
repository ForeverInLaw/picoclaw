package agent

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func buildModelListResolver(cfg *config.Config) func(raw string) (string, bool) {
	ensureProtocol := func(model string) string {
		model = strings.TrimSpace(model)
		if model == "" {
			return ""
		}
		if strings.Contains(model, "/") {
			return model
		}
		return "openai/" + model
	}

	return func(raw string) (string, bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" || cfg == nil {
			return "", false
		}

		if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && strings.TrimSpace(mc.Model) != "" {
			return ensureProtocol(mc.Model), true
		}

		for i := range cfg.ModelList {
			fullModel := strings.TrimSpace(cfg.ModelList[i].Model)
			if fullModel == "" {
				continue
			}
			if fullModel == raw {
				return ensureProtocol(fullModel), true
			}
			_, modelID := providers.ExtractProtocol(fullModel)
			if modelID == raw {
				return ensureProtocol(fullModel), true
			}
		}

		return "", false
	}
}

func resolveModelCandidates(
	cfg *config.Config,
	defaultProvider string,
	primary string,
	fallbacks []string,
) []providers.FallbackCandidate {
	return providers.ResolveCandidatesWithLookup(
		providers.ModelConfig{
			Primary:   primary,
			Fallbacks: fallbacks,
		},
		defaultProvider,
		buildModelListResolver(cfg),
	)
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
		for _, candidate := range cfg.ModelList {
			_, id := providers.ExtractProtocol(candidate.Model)
			if candidate.Model == modelName || id == modelName {
				modelCfg, err = cfg.GetModelConfig(candidate.ModelName)
				break
			}
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

// candidateProviderResolver keeps one resolved credential/endpoint per turn,
// including request-local retries, while preserving the injected primary client.
func candidateProviderResolver(cfg *config.Config, workspace string, primary providers.LLMProvider, candidates []providers.FallbackCandidate) func(string, string) (providers.LLMProvider, error) {
	clients := make(map[string]providers.LLMProvider)
	primaryProvider := ""
	if len(candidates) > 0 {
		primaryProvider = candidates[0].Provider
		clients[providers.ModelKey(primaryProvider, candidates[0].Model)] = primary
	}
	return func(provider, model string) (providers.LLMProvider, error) {
		key := providers.ModelKey(provider, model)
		if client := clients[key]; client != nil {
			return client, nil
		}
		name := ""
		for _, candidate := range candidates {
			if candidate.Provider == provider && candidate.Model == model {
				name = candidate.ConfigName
				break
			}
		}
		if name == "" && cfg != nil {
			for _, mc := range cfg.ModelList {
				protocol, id := providers.ExtractProtocol(mc.Model)
				if protocol == provider && id == model {
					name = mc.ModelName
					break
				}
			}
		}
		if name == "" {
			// Programmatically supplied candidates may share an injected client.
			if provider == primaryProvider {
				clients[key] = primary
				return primary, nil
			}
			return nil, fmt.Errorf("no model configuration for fallback %s/%s", provider, model)
		}
		mc, err := resolvedModelConfig(cfg, name, workspace)
		if err != nil {
			return nil, err
		}
		client, _, err := providers.CreateProviderFromConfig(mc)
		if err != nil {
			return nil, err
		}
		clients[key] = client
		return client, nil
	}
}
