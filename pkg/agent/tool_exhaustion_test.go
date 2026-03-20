package agent

import (
	"strings"
	"testing"
)

func TestToolExhaustionFallback_UsesLastToolResult(t *testing.T) {
	got := toolExhaustionFallback("ru", "old_text is required", true)
	if !strings.Contains(got, "old_text is required") {
		t.Fatalf("fallback=%q missing last tool result", got)
	}
	if !strings.Contains(got, "Последний результат tool") {
		t.Fatalf("fallback=%q missing localized intro", got)
	}
}

func TestToolExhaustionFallback_WithoutToolCallsFallsBackToDefault(t *testing.T) {
	got := toolExhaustionFallback("ru", "", false)
	if got != defaultResponse {
		t.Fatalf("fallback=%q want defaultResponse", got)
	}
}
