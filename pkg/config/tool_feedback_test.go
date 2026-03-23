package config

import "testing"

func TestAgentDefaultsShouldSendToolFeedback_DefaultCompatibility(t *testing.T) {
	cfg := AgentDefaults{
		ToolFeedback: ToolFeedbackConfig{
			Enabled: true,
		},
	}

	if !cfg.ShouldSendToolFeedback("group", "telegram:123") {
		t.Fatal("expected legacy behavior to allow tool feedback when no allowlist is configured")
	}
}

func TestAgentDefaultsShouldSendToolFeedback_DirectAllowlist(t *testing.T) {
	cfg := AgentDefaults{
		ToolFeedback: ToolFeedbackConfig{
			Enabled:         true,
			DirectAllowFrom: FlexibleStringSlice{"480546776", "telegram:6669548787"},
		},
	}

	if cfg.ShouldSendToolFeedback("group", "telegram:480546776") {
		t.Fatal("expected group tool feedback to be disabled when direct allowlist is configured")
	}
	if !cfg.ShouldSendToolFeedback("direct", "telegram:480546776") {
		t.Fatal("expected raw sender id to match allowlist in direct chat")
	}
	if !cfg.ShouldSendToolFeedback("direct", "telegram:6669548787") {
		t.Fatal("expected canonical sender id to match allowlist in direct chat")
	}
	if cfg.ShouldSendToolFeedback("direct", "telegram:999999999") {
		t.Fatal("expected non-allowlisted direct sender to be denied")
	}
}
