package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const keySearchSuccess = `{"results":[{"title":"Healthy key","url":"https://example.com/healthy","highlights":["repaired search"],"excerpts":["repaired search"]}]}`

func keySearchOptions(primary, primaryURL, backupURL string, primaryKeys, backupKeys []string) WebSearchToolOptions {
	parallelURL, exaURL, parallelKeys, exaKeys := primaryURL, backupURL, primaryKeys, backupKeys
	if primary == "exa" {
		parallelURL, exaURL, parallelKeys, exaKeys = backupURL, primaryURL, backupKeys, primaryKeys
	}
	return WebSearchToolOptions{
		PrimaryProvider: primary, ParallelEnabled: true, ParallelBaseURL: parallelURL, ParallelAPIKeys: parallelKeys,
		ExaEnabled: true, ExaBaseURL: exaURL, ExaAPIKeys: exaKeys,
	}
}

func TestWebSearch_RepairsPrimaryCredentialsBeforeProviderFallback(t *testing.T) {
	for _, provider := range []string{"parallel", "exa"} {
		for _, failure := range []struct {
			status int
			body   string
		}{
			{401, `{"error":"invalid_api_key"}`},
			{402, `{"tag":"NO_MORE_CREDITS"}`},
			{429, `{"error":"quota_exceeded"}`},
			{429, `{"tag":"RATE_LIMIT_EXCEEDED"}`},
		} {
			t.Run(fmt.Sprintf("%s/status=%d", provider, failure.status), func(t *testing.T) {
				keys := make(chan string, 4)
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					key := r.Header.Get("X-Api-Key")
					keys <- key
					if key == "broken-key" {
						w.WriteHeader(failure.status)
						_, _ = w.Write([]byte(failure.body))
						return
					}
					_, _ = w.Write([]byte(keySearchSuccess))
				}))
				defer primary.Close()
				var backupCalls atomic.Int32
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					backupCalls.Add(1)
					_, _ = w.Write([]byte(keySearchSuccess))
				}))
				defer backup.Close()
				tool, err := NewWebSearchTool(keySearchOptions(provider, primary.URL, backup.URL,
					[]string{"broken-key", "healthy-key"}, []string{"backup-key"}))
				if err != nil {
					t.Fatal(err)
				}
				result := tool.Execute(t.Context(), map[string]any{"query": "repair credentials"})
				if result.IsError || !strings.Contains(result.ForLLM, "repaired search") || backupCalls.Load() != 0 {
					t.Fatalf("result=%+v backup calls=%d", result, backupCalls.Load())
				}
				if len(keys) != 2 || <-keys != "broken-key" || <-keys != "healthy-key" {
					t.Fatal("search must try the configured healthy key after the credential failure")
				}
			})
		}
	}
}

func TestWebSearch_RepairsBackupKeysOnlyAfterTransientPrimaryFailure(t *testing.T) {
	for _, provider := range []string{"parallel", "exa"} {
		t.Run(provider, func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(503)
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				if r.Header.Get("X-Api-Key") == "broken-backup" {
					w.WriteHeader(401)
					return
				}
				_, _ = w.Write([]byte(keySearchSuccess))
			}))
			defer backup.Close()
			tool, err := NewWebSearchTool(keySearchOptions(provider, primary.URL, backup.URL,
				[]string{"primary-one", "primary-two"}, []string{"broken-backup", "healthy-backup"}))
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan *ToolResult, 1)
			go func() { result <- tool.Execute(t.Context(), map[string]any{"query": "fallback credentials"}) }()
			<-started
			if backupCalls.Load() != 0 {
				t.Fatal("backup started while primary was still pending")
			}
			close(release)
			got := <-result
			if got.IsError || !strings.Contains(got.ForLLM, "repaired search") || primaryCalls.Load() != 1 ||
				backupCalls.Load() != 2 {
				t.Fatalf("result=%+v primary=%d backup=%d", got, primaryCalls.Load(), backupCalls.Load())
			}
		})
	}
}

func TestWebSearch_KeySweepStopsAtPermanentRequestOrExhaustedCredentials(t *testing.T) {
	for _, provider := range []string{"parallel", "exa"} {
		for _, failure := range []struct {
			status   int
			body     string
			attempts int
			backup   bool
		}{
			{401, `{"error":"invalid_api_key"}`, 2, false},
			{402, `{"tag":"API_KEY_BUDGET_EXCEEDED"}`, 2, false},
			{429, `{"error":"insufficient_quota"}`, 2, false},
			{403, `{"error":"invalid_api_key"}`, 2, false},
			{403, `{"tag":"PROHIBITED_CONTENT"}`, 1, false},
			{400, `{"error":"invalid query"}`, 1, false},
			{429, `{"tag":"RATE_LIMIT_EXCEEDED"}`, 2, true},
			{503, `{"error":"unavailable"}`, 1, true},
		} {
			t.Run(fmt.Sprintf("%s/%d/%s", provider, failure.status, failure.body), func(t *testing.T) {
				var primaryCalls, backupCalls atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					primaryCalls.Add(1)
					w.WriteHeader(failure.status)
					_, _ = w.Write([]byte(failure.body))
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					_, _ = w.Write([]byte(keySearchSuccess))
				}))
				defer backup.Close()
				tool, err := NewWebSearchTool(
					keySearchOptions(provider, primary.URL, backup.URL, []string{"one", "two"}, []string{"backup"}),
				)
				if err != nil {
					t.Fatal(err)
				}
				result := tool.Execute(t.Context(), map[string]any{"query": "permanent failure"})
				wantBackup := int32(0)
				if failure.backup {
					wantBackup = 1
				}
				if result.IsError != !failure.backup || primaryCalls.Load() != int32(failure.attempts) ||
					backupCalls.Load() != wantBackup {
					t.Fatalf("result=%+v primary=%d backup=%d", result, primaryCalls.Load(), backupCalls.Load())
				}
			})
		}
	}
}

func TestWebSearch_CanceledKeyRepairDoesNotContinueOrFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		cancel()
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1) }))
	defer backup.Close()
	tool, err := NewWebSearchTool(
		keySearchOptions("parallel", primary.URL, backup.URL, []string{"bad", "pending", "unused"}, []string{"backup"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(ctx, map[string]any{"query": "cancel key repair"})
	if !result.IsError || calls.Load() != 2 || backupCalls.Load() != 0 {
		t.Fatalf("result=%+v primary=%d backup=%d", result, calls.Load(), backupCalls.Load())
	}
}

func TestWebSearch_KeyRepairHasOneOverallTwentySecondDeadlineWithoutBackup(t *testing.T) {
	var calls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		select {
		case <-time.After(7 * time.Second):
			w.WriteHeader(401)
		case <-r.Context().Done():
		}
	}))
	defer primary.Close()
	tool, err := NewWebSearchTool(keySearchOptions("parallel", primary.URL, "", []string{"one", "two", "three"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result := tool.Execute(t.Context(), map[string]any{"query": "bounded key repair"})
	if !result.IsError || calls.Load() != 3 || time.Since(start) > 21*time.Second {
		t.Fatalf("result=%+v requests=%d elapsed=%v", result, calls.Load(), time.Since(start))
	}
}
