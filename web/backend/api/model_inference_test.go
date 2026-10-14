package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func modelInferenceDashboard(t *testing.T, models []config.ModelConfig) (http.Handler, *http.Cookie, string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.ModelList = models
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	sessions := middleware.NewLauncherDashboardSessions()
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	h := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux)
	w := httptest.NewRecorder()
	if err := sessions.Issue(w, httptest.NewRequest(http.MethodGet, "/", nil), nil); err != nil {
		t.Fatal(err)
	}
	return h, w.Result().Cookies()[0], path
}

func TestModelInferenceCancellationStopsCurrentHTTPCall(t *testing.T) {
	started, disconnected := make(chan struct{}), make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-time.After(2 * time.Second):
		}
	}))
	defer endpoint.Close()
	h, cookie, _ := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ModelName: "selected", Model: "openai/selected", APIBase: endpoint.URL + "/v1", APIKey: "selected-secret"},
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/api/models/0/test", nil).WithContext(ctx)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("inference never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled action kept running")
	}
	if w.Code != 502 || !strings.Contains(w.Body.String(), "canceled") {
		t.Fatalf("cancel outcome: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach configured endpoint")
	}
}

func TestModelInferenceReportsSanitizedFailuresWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body, guidance string
	}{
		{"auth", 401, `{"error":{"message":"invalid selected-secret at https://private.example"}}`, "Authentication failed"},
		{"endpoint", 404, `{"error":{"message":"private selected-secret endpoint"}}`, "not found"},
		{"image", 400, `{"error":{"message":"unsupported image selected-secret"}}`, "does not support these image"},
		{"empty", 200, `{"choices":[{"message":{"content":"","reasoning_content":"thought only"}}]}`, "no answer"},
		{"timeout", 200, "", "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.name == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer endpoint.Close()
			h, cookie, _ := modelInferenceDashboard(
				t,
				[]config.ModelConfig{
					{
						ModelName:      "selected",
						Model:          "openai/selected-model",
						APIBase:        endpoint.URL + "/v1",
						APIKey:         "selected-secret",
						RequestTimeout: 1,
					},
				},
			)
			w := dashboardRequest(h, "POST", "/api/models/0/test", "", cookie)
			if w.Code != 502 || !strings.Contains(w.Body.String(), tc.guidance) ||
				strings.Contains(w.Body.String(), "selected-secret") ||
				strings.Contains(w.Body.String(), "private.example") {
				t.Fatalf("failure outcome: %d %s", w.Code, w.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("failed inference operations=%d", calls.Load())
			}
		})
	}
}

func TestSavedMultiKeyAndFileCredentialsUseOnlySelectedRuntimeEntry(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer second-file-secret" {
			t.Error("probe rotated or borrowed another key")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer endpoint.Close()
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{
				ModelName: "multi",
				Model:     "openai/model",
				APIBase:   endpoint.URL + "/v1",
				APIKeys:   []string{"first-secret", "file://key.txt"},
			},
		},
	)
	if err := os.WriteFile(
		filepath.Join(filepath.Dir(path), "key.txt"),
		[]byte("second-file-secret"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	// Multi-key expansion puts the second key at runtime index zero.
	if w := dashboardRequest(h, "POST", "/api/models/0/test", "", cookie); w.Code != 200 {
		t.Fatalf("multi-key selected probe: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("operations=%d", calls.Load())
	}
}

func TestAuthenticatedSavedModelInferenceUsesSelectedIdentityOnce(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"wrong-model"},{"id":"selected-model"}]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected operation: %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer selected-secret" {
			t.Error("request did not use the selected configuration's credential")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens int             `json:"max_tokens"`
			Tools     json.RawMessage `json:"tools"`
			Stream    bool            `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "selected-model" || len(body.Messages) != 1 || body.Messages[0].Role != "user" ||
			body.Messages[0].Content != "Reply with OK." || body.MaxTokens != 32 || len(body.Tools) != 0 || body.Stream {
			t.Errorf("probe was not a minimal selected model request: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(
			[]byte(
				`{"choices":[{"message":{"role":"assistant","content":"OK selected-secret"},"finish_reason":"stop"}]}`,
			),
		)
	}))
	defer endpoint.Close()
	h, cookie, _ := modelInferenceDashboard(t, []config.ModelConfig{
		{ModelName: "same-alias", Model: "openai/wrong-model", APIBase: endpoint.URL + "/v1", APIKey: "other-secret"},
		{
			ModelName: "same-alias",
			Model:     "openai/selected-model",
			APIBase:   endpoint.URL + "/v1",
			APIKey:    "selected-secret",
		},
	})
	if w := dashboardRequest(h, "POST", "/api/models/1/test", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous probe: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("inference happened without an authorized action")
	}
	if w := dashboardRequest(h, "GET", "/api/models", "", cookie); w.Code != 200 {
		t.Fatalf("model status listing: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("model availability listing triggered inference")
	}
	w := dashboardRequest(h, "POST", "/api/models/1/test", "", cookie)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"success"`) {
		t.Fatalf("saved probe: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("inference operations=%d, want 1", calls.Load())
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("credential or raw provider content leaked to browser")
	}
}

func TestEditedModelValidationAndInferenceKeepCredentialAtSelectedEndpoint(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer selected-secret" {
			t.Error("compatible edit lost the selected saved credential")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer endpoint.Close()
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer replacement-secret" {
			t.Error("foreign endpoint received a borrowed credential")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer foreign.Close()
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{
		{
			ModelName: "selected",
			Model:     "openai/selected-model",
			APIBase:   endpoint.URL + "/v1",
			APIKey:    "selected-secret",
		},
	})
	request := map[string]any{
		"model_index": 0,
		"model_name":  "selected",
		"model":       "openai/selected-model",
		"api_base":    endpoint.URL + "/v1",
		"api_key":     "sel****cret",
	}
	post := func(route string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		return dashboardRequest(h, "POST", route, string(body), cookie)
	}
	if w := post("/api/models/validate"); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"valid"`) {
		t.Fatalf("validation: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("configuration validation sent inference")
	}
	if w := post("/api/models/test-inline"); w.Code != 200 {
		t.Fatalf("edited probe: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("edited inference operations=%d", calls.Load())
	}
	request["api_base"] = foreign.URL + "/v1"
	for _, key := range []string{"", "****", "sel****cret"} {
		request["api_key"] = key
		if w := post("/api/models/test-inline"); w.Code != 400 {
			t.Fatalf("changed endpoint: %d %s", w.Code, w.Body.String())
		}
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("changed endpoint was contacted with an omitted/masked key")
	}
	request["api_key"] = "replacement-secret"
	if w := post("/api/models/test-inline"); w.Code != 200 {
		t.Fatalf("explicit replacement: %d %s", w.Code, w.Body.String())
	}
	if foreignCalls.Load() != 1 {
		t.Fatal("explicit replacement did not send one request")
	}
	// Saving an endpoint edit must not leave the old key available to a later saved probe.
	request["api_key"] = ""
	body, _ := json.Marshal(request)
	if w := dashboardRequest(h, "PUT", "/api/models/0", string(body), cookie); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[0].APIKey != "" {
		t.Fatal("saved endpoint edit transferred old credential")
	}
}

func TestModelInferenceRejectsOverridesBeforeContactingEndpoint(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer endpoint.Close()
	h, cookie, _ := modelInferenceDashboard(t, nil)
	for _, extra := range []map[string]any{
		{"model": "another-model"}, {"messages": []any{}}, {"max_tokens": 100000}, {"stream": true}, {"tools": []any{}},
	} {
		body, _ := json.Marshal(
			config.ModelConfig{
				ModelName: "probe",
				Model:     "openai/selected",
				APIBase:   endpoint.URL + "/v1",
				APIKey:    "explicit-key",
				ExtraBody: extra,
			},
		)
		w := dashboardRequest(h, "POST", "/api/models/test-inline", string(body), cookie)
		if w.Code != 400 {
			t.Fatalf("override accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid minimal-probe config sent %d requests", calls.Load())
	}
}

func TestEditedModelNeverBorrowsSecretsAcrossProviderProxyOrSelectedEntry(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[{"id":"selected"}]}`))
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer selected-secret" {
			t.Error("credential reference was sent literally or borrowed")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer endpoint.Close()
	h, cookie, _ := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ModelName: "selected", Model: "openai/selected", APIBase: endpoint.URL + "/v1", APIKey: "selected-secret"},
		},
	)
	for _, change := range []map[string]any{
		{"model": "gemini/selected"}, {"proxy": "http://127.0.0.1:1"}, {"model_name": "stale-selection"}, {"model_index": 7},
	} {
		request := map[string]any{
			"model_index": 0,
			"model_name":  "selected",
			"model":       "openai/selected",
			"api_base":    endpoint.URL + "/v1",
		}
		for key, value := range change {
			request[key] = value
		}
		body, _ := json.Marshal(request)
		w := dashboardRequest(h, "POST", "/api/models/test-inline", string(body), cookie)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("incompatible selection accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid selection sent inference")
	}
	for _, placeholder := range []string{"****", "file://saved.key", "enc://****"} {
		body, _ := json.Marshal(
			map[string]any{
				"model_index": 0,
				"model_name":  "selected",
				"model":       "openai/selected",
				"api_base":    endpoint.URL + "/v1/",
				"api_key":     placeholder,
			},
		)
		w := dashboardRequest(h, "POST", "/api/models/test-inline", string(body), cookie)
		if w.Code != 200 {
			t.Fatalf("same-destination reference: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("compatible actions=%d", calls.Load())
	}
}
