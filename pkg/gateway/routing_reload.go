package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Routing is captured from disk per request. The optional watcher must not
// replace conversation resources merely because that routing was edited.
func routingOnlyChange(current, next *config.Config) bool {
	nonRouting := func(cfg *config.Config) ([]byte, error) {
		copyCfg := *cfg
		copyCfg.ModelList = nil
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
