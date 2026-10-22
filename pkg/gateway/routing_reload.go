package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Routing is captured from disk per request. The optional watcher must not
// replace conversation resources merely because that routing was edited.
func routingOnlyChange(current, next *config.Config) bool {
	nonRouting := func(cfg *config.Config) ([]byte, error) {
		copyCfg := *cfg
		copyCfg.ModelList = nil
		// Transcribers retain credentials/model settings until gateway reload.
		// Keep their model dependencies in the comparison while chat routing is
		// refreshed per request. Include all matches for a round-robin alias.
		if !cfg.Voice.Riva.Enabled || cfg.Providers.Nvidia.APIKey == "" {
			voiceModel := strings.TrimSpace(cfg.Voice.ModelName)
			groqFallback := cfg.Providers.Groq.APIKey == ""
			for _, model := range cfg.ModelList {
				selected := voiceModel != "" && (model.ModelName == voiceModel ||
					voiceModel == config.ModelIDPrefix+model.ID)
				groq := groqFallback && strings.HasPrefix(model.Model, "groq/") && model.APIKey != ""
				if selected || groq {
					copyCfg.ModelList = append(copyCfg.ModelList, model)
				}
				if groq {
					groqFallback = false
				}
			}
		}
		defaults := &copyCfg.Agents.Defaults
		defaults.Model = ""
		defaults.ModelName = ""
		defaults.Provider = ""
		defaults.ModelFallbacks = nil
		defaults.ImageModel = ""
		defaults.ImageModelFallbacks = nil
		defaults.Routing = nil
		copyCfg.Agents.List = append([]config.AgentConfig(nil), cfg.Agents.List...)
		for i := range copyCfg.Agents.List {
			copyCfg.Agents.List[i].Model = nil
		}
		return json.Marshal(&copyCfg)
	}
	before, beforeErr := nonRouting(current)
	after, afterErr := nonRouting(next)
	return beforeErr == nil && afterErr == nil && bytes.Equal(before, after)
}
