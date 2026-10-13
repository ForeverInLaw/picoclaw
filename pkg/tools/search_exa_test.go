package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebSearch_ExaContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" || r.Header.Get("X-Api-Key") != "exa-key" {
			t.Errorf("unexpected Exa request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Query      string `json:"query"`
			Type       string `json:"type"`
			NumResults int    `json:"numResults"`
			Contents   struct {
				Highlights struct {
					MaxCharacters int `json:"maxCharacters"`
				} `json:"highlights"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Query != "native formulas" || body.Type != "auto" || body.NumResults != 1 ||
			body.Contents.Highlights.MaxCharacters != 1500 {
			t.Errorf("unexpected body: %+v", body)
		}
		_, _ = w.Write(
			[]byte(
				`{"results":[{"title":"Formulas","url":"https://example.com/formulas","highlights":["Native mathematics."],"text":"Unused full page."}]}`,
			),
		)
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(
		WebSearchToolOptions{
			PrimaryProvider: "exa",
			ExaEnabled:      true,
			ExaAPIKeys:      []string{"exa-key"},
			ExaBaseURL:      server.URL,
			ExaMaxResults:   1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(t.Context(), map[string]any{"query": "native formulas"})
	if result.IsError || !strings.Contains(result.ForLLM, "Native mathematics.") ||
		!strings.Contains(result.ForLLM, "https://example.com/formulas") ||
		strings.Contains(result.ForLLM, "Unused full page") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWebSearch_TransientFallback(t *testing.T) {
	for _, primary := range []string{"", "parallel", "exa"} {
		for _, status := range []int{200, 400, 401, 402, 429, 503} {
			t.Run(fmt.Sprintf("primary=%s/status=%d", primary, status), func(t *testing.T) {
				parallelCalls, exaCalls := 0, 0
				primaryIsExa := primary == "exa"
				serve := func(name string, calls *int) *httptest.Server {
					return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						*calls++
						if (name == "exa") == primaryIsExa && status != 200 {
							w.WriteHeader(status)
							return
						}
						_, _ = w.Write(
							[]byte(
								`{"results":[{"title":"Formulas","url":"https://example.com/formulas","highlights":["Native mathematics."],"excerpts":["Native mathematics."]}]}`,
							),
						)
					}))
				}
				parallel, exa := serve("parallel", &parallelCalls), serve("exa", &exaCalls)
				defer parallel.Close()
				defer exa.Close()
				tool, err := NewWebSearchTool(
					WebSearchToolOptions{
						PrimaryProvider: primary,
						ParallelEnabled: true,
						ParallelAPIKeys: []string{"p-key"},
						ParallelBaseURL: parallel.URL,
						ExaEnabled:      true,
						ExaAPIKeys:      []string{"e-key"},
						ExaBaseURL:      exa.URL,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				result := tool.Execute(t.Context(), map[string]any{"query": "native formulas"})
				wantFallback := status == 429 || status == 503
				if result.IsError != (status != 200 && !wantFallback) {
					t.Fatalf("wrong result: %+v", result)
				}
				wantP, wantE := 1, 0
				if primaryIsExa {
					wantP, wantE = 0, 1
				}
				if wantFallback {
					wantP, wantE = 1, 1
				}
				if parallelCalls != wantP || exaCalls != wantE {
					t.Fatalf("parallel=%d exa=%d, want %d/%d", parallelCalls, exaCalls, wantP, wantE)
				}
				if wantFallback {
					wantName := "Exa"
					if primaryIsExa {
						wantName = "Parallel"
					}
					if !strings.Contains(result.ForLLM, "via "+wantName) {
						t.Fatalf("incorrect source attribution: %+v", result)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				result = tool.Execute(ctx, map[string]any{"query": "native formulas"})
				if !result.IsError || parallelCalls != wantP || exaCalls != wantE {
					t.Fatal("cancellation made extra external requests")
				}
			})
		}
	}
}

func TestWebSearch_PermanentEndpointFailuresDoNotCallBackup(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	for _, endpoint := range []string{"http://127.0.0.1:999999", "unsupported://local", tlsServer.URL} {
		t.Run(endpoint, func(t *testing.T) {
			var backupCalls atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backupCalls.Add(1)
				_, _ = w.Write(
					[]byte(`{"results":[{"title":"Backup","url":"https://example.com","highlights":["backup hit"]}]}`),
				)
			}))
			defer backup.Close()
			tool, err := NewWebSearchTool(WebSearchToolOptions{
				PrimaryProvider: "parallel", ParallelEnabled: true, ParallelAPIKeys: []string{"p-key"},
				ParallelBaseURL: endpoint, ExaEnabled: true, ExaAPIKeys: []string{"e-key"}, ExaBaseURL: backup.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := tool.Execute(t.Context(), map[string]any{"query": "configured endpoint"})
			if !result.IsError || backupCalls.Load() != 0 {
				t.Fatalf("permanent endpoint failure result=%+v backup calls=%d", result, backupCalls.Load())
			}
		})
	}
}

func TestWebSearch_MissingCredentialsAndExhaustedFallback(t *testing.T) {
	for _, primary := range []string{"parallel", "exa", "unknown"} {
		if _, err := NewWebSearchTool(
			WebSearchToolOptions{PrimaryProvider: primary, DuckDuckGoEnabled: true},
		); err == nil {
			t.Errorf("%s without credentials must fail clearly", primary)
		}
	}
	legacy, err := NewWebSearchTool(WebSearchToolOptions{DuckDuckGoEnabled: true})
	if err != nil || legacy == nil {
		t.Fatalf("legacy config broken: %v", err)
	}
	for _, tc := range []struct {
		name, body string
		backup     bool
	}{
		{"unavailable backup", "", false},
		{"both fail", "", true},
		{"permanent quota", `{"error":"insufficient_quota"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primaryCalls, backupCalls := 0, 0
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				primaryCalls++
				w.WriteHeader(429)
				_, _ = w.Write([]byte(tc.body))
			}))
			backup := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { backupCalls++; w.WriteHeader(503) }),
			)
			defer primary.Close()
			defer backup.Close()
			opts := WebSearchToolOptions{
				ParallelEnabled: true,
				ParallelAPIKeys: []string{"key"},
				ParallelBaseURL: primary.URL,
			}
			if tc.backup {
				opts.ExaEnabled = true
				opts.ExaAPIKeys = []string{"key"}
				opts.ExaBaseURL = backup.URL
			}
			tool, err := NewWebSearchTool(opts)
			if err != nil {
				t.Fatal(err)
			}
			result := tool.Execute(t.Context(), map[string]any{"query": "formulas"})
			wantBackup := 0
			if tc.backup && tc.body == "" {
				wantBackup = 1
			}
			if !result.IsError || primaryCalls != 1 || backupCalls != wantBackup {
				t.Fatalf("calls=%d/%d result=%+v", primaryCalls, backupCalls, result)
			}
			if wantBackup == 1 &&
				(!strings.Contains(result.ForLLM, "Parallel") || !strings.Contains(result.ForLLM, "Exa")) {
				t.Fatalf("both failures must be visible: %s", result.ForLLM)
			}
		})
	}
}
