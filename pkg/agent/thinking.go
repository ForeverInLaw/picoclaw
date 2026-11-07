package agent

import "strings"

// ThinkingLevel is the configured thinking_level, forwarded to providers that
// support thinking; each provider maps it onto its own parameters.
//
//   - "low"/"medium"/"high"/"xhigh"/"max": think with that effort
//   - "adaptive": the provider's adaptive thinking
//   - "off": no thinking, or the least the model allows
//   - unset: nothing is sent and the model's default applies
type ThinkingLevel string

const (
	ThinkingDefault  ThinkingLevel = ""
	ThinkingOff      ThinkingLevel = "off"
	ThinkingLow      ThinkingLevel = "low"
	ThinkingMedium   ThinkingLevel = "medium"
	ThinkingHigh     ThinkingLevel = "high"
	ThinkingXHigh    ThinkingLevel = "xhigh"
	ThinkingMax      ThinkingLevel = "max"
	ThinkingAdaptive ThinkingLevel = "adaptive"
)

// parseThinkingLevel normalizes a config string to a ThinkingLevel.
// Case-insensitive and whitespace-tolerant for user-facing config values.
// Returns ThinkingDefault for unknown or empty values.
func parseThinkingLevel(level string) ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "adaptive":
		return ThinkingAdaptive
	case "low":
		return ThinkingLow
	case "medium":
		return ThinkingMedium
	case "high":
		return ThinkingHigh
	case "xhigh":
		return ThinkingXHigh
	case "max":
		return ThinkingMax
	case "off":
		return ThinkingOff
	default:
		return ThinkingDefault
	}
}
