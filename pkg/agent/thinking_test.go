package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestParseThinkingLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ThinkingLevel
	}{
		{"off", "off", ThinkingOff},
		{"empty", "", ThinkingDefault},
		{"low", "low", ThinkingLow},
		{"medium", "medium", ThinkingMedium},
		{"high", "high", ThinkingHigh},
		{"xhigh", "xhigh", ThinkingXHigh},
		{"max", "max", ThinkingMax},
		{"adaptive", "adaptive", ThinkingAdaptive},
		{"unknown", "unknown", ThinkingDefault},
		{"upper_OFF", "OFF", ThinkingOff},
		// Case-insensitive and whitespace-tolerant
		{"upper_Medium", "Medium", ThinkingMedium},
		{"upper_HIGH", "HIGH", ThinkingHigh},
		{"mixed_Adaptive", "Adaptive", ThinkingAdaptive},
		{"leading_space", " high", ThinkingHigh},
		{"trailing_space", "low ", ThinkingLow},
		{"both_spaces", " medium ", ThinkingMedium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseThinkingLevel(tt.input); got != tt.want {
				t.Errorf("parseThinkingLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// thinkingProvider is a provider that accepts thinking_level.
type thinkingProvider struct{ mockProvider }

func (thinkingProvider) SupportsThinking() bool { return true }

func TestPrepareCandidateRequestForwardsConfiguredThinkingLevel(t *testing.T) {
	tests := []struct {
		level ThinkingLevel
		want  any // nil means thinking_level is not sent
	}{
		{ThinkingDefault, nil},
		{ThinkingOff, "off"},
		{ThinkingMax, "max"},
		{ThinkingHigh, "high"},
	}
	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			_, opts := prepareCandidateRequest(&config.Config{}, &thinkingProvider{}, providers.FallbackCandidate{},
				nil, nil, tt.level)
			if got := opts["thinking_level"]; got != tt.want {
				t.Errorf("thinking_level = %v, want %v", got, tt.want)
			}
		})
	}
}
