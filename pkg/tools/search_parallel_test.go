package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearch_ParallelContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" || r.Header.Get("x-api-key") != "parallel-key" {
			t.Errorf("unexpected request: %s %s, authentication present=%v", r.Method, r.URL.Path, r.Header.Get("x-api-key") == "parallel-key")
		}
		var body struct {
			SearchQueries    []string `json:"search_queries"`
			Mode             string   `json:"mode"`
			AdvancedSettings struct {
				MaxResults      int `json:"max_results"`
				ExcerptSettings struct {
					MaxCharsPerResult int `json:"max_chars_per_result"`
				} `json:"excerpt_settings"`
			} `json:"advanced_settings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.SearchQueries) != 1 || body.SearchQueries[0] != "native formulas" || body.Mode != "fast" || body.AdvancedSettings.MaxResults != 1 || body.AdvancedSettings.ExcerptSettings.MaxCharsPerResult != 1500 {
			t.Errorf("unexpected request body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Formulas","url":"https://example.com/formulas","excerpts":["Native mathematics.","Display expressions."]},{"title":"Extra","url":"https://example.com/extra","excerpts":["Omit this."]}]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{ParallelEnabled: true, ParallelAPIKeys: []string{"parallel-key"}, ParallelBaseURL: server.URL, ParallelMaxResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tool == nil {
		t.Fatal("Parallel credentials must enable web_search")
	}
	result := tool.Execute(t.Context(), map[string]any{"query": "native formulas"})
	for _, want := range []string{"Formulas", "https://example.com/formulas", "Native mathematics.", "Display expressions."} {
		if result.IsError || !strings.Contains(result.ForLLM, want) {
			t.Fatalf("missing %q in result: %+v", want, result)
		}
	}
	if strings.Contains(result.ForLLM, "Omit this") {
		t.Fatalf("result count exceeded: %s", result.ForLLM)
	}
}

func TestWebSearch_ParallelFailureAndCancellation(t *testing.T) {
	for _, status := range []int{400, 401, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(status) }))
			defer server.Close()
			tool, err := NewWebSearchTool(WebSearchToolOptions{PrimaryProvider: "parallel", ParallelEnabled: true, ParallelAPIKeys: []string{"key"}, ParallelBaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			result := tool.Execute(t.Context(), map[string]any{"query": "formula"})
			if !result.IsError || !strings.Contains(result.ForLLM, http.StatusText(status)) && !strings.Contains(result.ForLLM, "HTTP") || calls != 1 {
				t.Fatalf("expected one clear failure; calls=%d result=%+v", calls, result)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			result = tool.Execute(ctx, map[string]any{"query": "formula"})
			if !result.IsError || calls != 1 {
				t.Fatalf("canceled search made request: calls=%d result=%+v", calls, result)
			}
		})
	}
}
