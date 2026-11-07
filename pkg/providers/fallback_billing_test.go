package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	anthropicprovider "github.com/sipeed/picoclaw/pkg/providers/anthropic"
)

// anthropicErrorBody renders Anthropic's error envelope around message.
func anthropicErrorBody(message string) string {
	return `{"type":"error","error":{"type":"invalid_request_error","message":"` + message + `"}}`
}

const anthropicCreditBalanceMessage = "Your credit balance is too low to access the Anthropic API. " +
	"Please go to Plans & Billing to upgrade or purchase credits."

// anthropicProviders builds each way picoclaw talks to Anthropic with an API key,
// all pointed at a fake Anthropic that answers every request with status and body.
func anthropicProviders(t *testing.T, status int, body string) map[string]LLMProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	configured, _, err := CreateProviderFromConfig(&config.ModelConfig{
		ModelName: "claude",
		Model:     "anthropic/claude-sonnet-4.6",
		APIKey:    "sk-ant-key-1",
		APIBase:   server.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	return map[string]LLMProvider{
		"anthropic configured model": configured,
		"messages API":               anthropicprovider.NewProviderWithBaseURL("sk-ant-key-1", server.URL),
	}
}

// runAnthropicThenReserve executes a Routing chain whose primary is the given
// Anthropic provider and whose reserve always answers.
func runAnthropicThenReserve(
	t *testing.T, provider LLMProvider,
) (result *FallbackResult, reserveCalls int, cooldown *CooldownTracker, err error) {
	t.Helper()
	primary := FallbackCandidate{Provider: "anthropic", Model: "claude-sonnet-4.6"}
	reserve := FallbackCandidate{Provider: "codex", Model: "gpt-5.5"}
	cooldown = NewCooldownTracker()
	result, err = NewFallbackChain(cooldown).ExecuteCandidate(
		context.Background(),
		[]FallbackCandidate{primary, reserve},
		func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
			if c == reserve {
				reserveCalls++
				return &LLMResponse{Content: "from reserve"}, nil
			}
			return provider.Chat(ctx, []Message{{Role: "user", Content: "hi"}}, nil, c.Model, nil)
		},
	)
	return result, reserveCalls, cooldown, err
}

func TestFallbackChain_AnthropicBilling400MovesToNextCandidate(t *testing.T) {
	messages := map[string]string{
		"credit balance":  anthropicCreditBalanceMessage,
		"payment":         "Payment required to continue.",
		"plans & billing": "Visit Plans & Billing to add a payment method.",
		"balance":         "Insufficient balance on this organization.",
	}
	for phrasing, message := range messages {
		for name, provider := range anthropicProviders(t, http.StatusBadRequest, anthropicErrorBody(message)) {
			t.Run(phrasing+"/"+name, func(t *testing.T) {
				result, reserveCalls, cooldown, err := runAnthropicThenReserve(t, provider)
				if err != nil {
					t.Fatalf("Execute() error = %v, want reserve to answer", err)
				}
				if reserveCalls != 1 || result.Response.Content != "from reserve" {
					t.Fatalf("reserve calls = %d, response = %+v; want one reserve answer",
						reserveCalls, result.Response)
				}
				if got := result.Attempts[0].Reason; got != FailoverBilling {
					t.Fatalf("primary attempt reason = %q, want %q", got, FailoverBilling)
				}
				primaryKey := result.Attempts[0].Candidate.StableKey()
				if remaining := cooldown.CooldownRemaining(primaryKey); remaining < time.Hour {
					t.Fatalf("primary cooldown = %s, want billing cooldown of hours", remaining)
				}
			})
		}
	}
}

func TestFallbackChain_AnthropicMalformedRequest400AbortsChain(t *testing.T) {
	body := anthropicErrorBody("messages.402.content: Field required")
	for name, provider := range anthropicProviders(t, http.StatusBadRequest, body) {
		t.Run(name, func(t *testing.T) {
			_, reserveCalls, _, err := runAnthropicThenReserve(t, provider)
			var failure *FailoverError
			if !errors.As(err, &failure) || failure.Reason != FailoverFormat {
				t.Fatalf("Execute() error = %v, want format failure", err)
			}
			if reserveCalls != 0 {
				t.Fatalf("reserve calls = %d, want chain aborted before reserve", reserveCalls)
			}
		})
	}
}
