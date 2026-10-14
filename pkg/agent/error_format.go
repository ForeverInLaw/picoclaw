package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// safeErrorText applies mandatory credential protection before optional filtering.
// Provider failures use repair guidance rather than untrusted response bodies.
func (al *AgentLoop) safeErrorText(err error) string {
	if guidance := providers.ErrorGuidance(err); guidance != "" {
		return guidance
	}
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, mc := range al.cfg.ModelList {
		for _, key := range append([]string{mc.APIKey}, mc.APIKeys...) {
			if key != "" {
				text = strings.ReplaceAll(text, key, "[REDACTED]")
			}
		}
	}
	return al.cfg.FilterSensitiveData(text)
}
