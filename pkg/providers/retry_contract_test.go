package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexSDKRetryDisabledAndServerAdvicePreserved(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","message":"try later","type":"rate_limit_exceeded"}}`)
	}))
	defer server.Close()
	p := NewCodexProvider("test-token", "")
	p.client = createOpenAITestClient(server.URL, "test-token", "")
	_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-4o", nil)
	failure := ClassifyError(err, "openai", "gpt-4o")
	if requests.Load() != 1 || failure == nil || !failure.IsTransient() || failure.RetryAfter != 7*time.Second {
		t.Fatalf("requests=%d failure=%+v", requests.Load(), failure)
	}
}

func TestHTTPModelErrorsPreserveRateLimitAdviceAndQuotaMeaning(t *testing.T) {
	for _, code := range []string{"rate_limit_exceeded", "insufficient_quota"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", time.Now().Add(8*time.Second).UTC().Format(http.TimeFormat))
				w.WriteHeader(429)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "retry meaning comes from API code"}})
			}))
			defer server.Close()
			p := NewHTTPProvider("test-token", server.URL, "")
			_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, "model", nil)
			failure := ClassifyError(err, "openai", "model")
			if failure == nil || failure.Status != 429 || failure.RetryAfter < 6*time.Second || failure.RetryAfter > 8*time.Second {
				t.Fatalf("lost HTTP retry metadata: %+v", failure)
			}
			if failure.IsTransient() != (code == "rate_limit_exceeded") {
				t.Fatalf("incorrect quota/rate-limit classification: %+v", failure)
			}
		})
	}
}

func TestClaudeSDKRetryDisabledAndServerAdvicePreserved(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"try later"}}`)
	}))
	defer server.Close()
	p := NewClaudeProviderWithBaseURL("test-token", server.URL)
	_, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, "claude-test", nil)
	failure := ClassifyError(err, "anthropic", "claude-test")
	if requests.Load() != 1 || failure == nil || !failure.IsTransient() || failure.RetryAfter != 4*time.Second {
		t.Fatalf("requests=%d failure=%+v", requests.Load(), failure)
	}
}
