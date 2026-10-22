package providers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
)

// ModelConfigKey identifies a configured endpoint and credential selection
// without exposing its secrets or depending on model-list ordering.
func ModelConfigKey(mc *config.ModelConfig) string {
	if mc == nil {
		return ""
	}
	clone := *mc
	clone.Fallbacks = nil              // Routing policy does not change this provider client.
	clone.InactivityTimeoutSeconds = 0 // Wait policy preserves provider/account health.
	protocol, _ := ExtractProtocol(clone.Model)
	switch protocol {
	case "claude-cli", "claudecli", "codex-cli", "codexcli":
	default:
		clone.Workspace = "" // HTTP clients do not depend on the injected agent workspace.
	}
	payload, err := json.Marshal(clone)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("config:%x", sha256.Sum256(payload))
}
