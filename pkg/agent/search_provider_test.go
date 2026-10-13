package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type searchScenarioProvider struct{ calls int }

func (p *searchScenarioProvider) GetDefaultModel() string    { return "search-model" }
func (p *searchScenarioProvider) SupportsNativeSearch() bool { return true }
func (p *searchScenarioProvider) Chat(_ context.Context, messages []providers.Message, definitions []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.calls++
	if p.calls == 1 {
		for _, definition := range definitions {
			if definition.Function.Name == "web_search" {
				return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{ID: "search-1", Name: "web_search", Arguments: map[string]any{"query": "native formulas"}}}}, nil
			}
		}
		return nil, fmt.Errorf("configured external web_search was hidden by native search")
	}
	for _, message := range messages {
		if message.Role == "tool" && strings.Contains(message.Content, "Native mathematics.") && strings.Contains(message.Content, "https://example.com/formulas") {
			return &providers.LLMResponse{Content: "Native mathematics. Source: https://example.com/formulas"}, nil
		}
	}
	return nil, fmt.Errorf("search sources missing from model context")
}

func TestAgentSearch_ConfiguredDirectAndInline(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, primary := range []string{"parallel", "exa"} {
			t.Run(fmt.Sprintf("inline=%v/primary=%s", inline, primary), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests++
					_, _ = w.Write([]byte(`{"results":[{"title":"Formulas","url":"https://example.com/formulas","excerpts":["Native mathematics."],"highlights":["Native mathematics."]}]}`))
				}))
				defer server.Close()
				failures := 0
				failedPrimary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { failures++; w.WriteHeader(503) }))
				defer failedPrimary.Close()
				cfg := config.DefaultConfig()
				cfg.Agents.Defaults.Workspace = t.TempDir()
				cfg.Agents.Defaults.ModelName = "search-model"
				cfg.Tools.Web.PrimaryProvider = primary
				cfg.Tools.Web.Parallel = config.ParallelSearchConfig{Enabled: true, APIKey: "test-key", BaseURL: server.URL}
				cfg.Tools.Web.Exa = config.ExaSearchConfig{Enabled: true, APIKey: "test-key", BaseURL: server.URL}
				if primary == "parallel" {
					cfg.Tools.Web.Parallel.BaseURL = failedPrimary.URL
				} else {
					cfg.Tools.Web.Exa.BaseURL = failedPrimary.URL
				}
				provider := &searchScenarioProvider{}
				loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
				chatID := "42"
				if inline {
					chatID = "inline:destination"
				}
				answer, err := loop.ProcessDirectWithChannel(t.Context(), "find native formulas", "search-scenario", "telegram", chatID)
				if err != nil || !strings.Contains(answer, "https://example.com/formulas") || requests != 1 || failures != 1 {
					t.Fatalf("answer=%q err=%v requests=%d", answer, err, requests)
				}
			})
		}
	}
}
