package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCodexLegacyConnectionMigratesWithoutChangingAPIKeyModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	if err := os.WriteFile(
		filepath.Join(home, "auth.json"),
		[]byte(
			`{"credentials":{"openai":{"access_token":"legacy-secret","refresh_token":"refresh-secret","account_id":"workspace-1","provider":"openai","auth_method":"oauth","email":"owner@example.com"}}}`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, configPath := modelInferenceDashboard(t, []config.ModelConfig{
		{ModelName: "subscription", Model: "openai/gpt-5.4", AuthMethod: "oauth"},
		{ModelName: "paid-api", Model: "openai/gpt-5.4", APIBase: "https://example.com/v1", APIKey: "api-secret"},
	})
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/oauth/accounts", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", w.Code)
	}
	w := request("GET", "/api/oauth/accounts", "", true)
	if w.Code != 200 {
		t.Fatalf("accounts = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("list leaked tokens: %s", w.Body.String())
	}
	var list struct {
		Accounts []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Accounts) != 1 || list.Accounts[0].ID == "" || list.Accounts[0].Label != "owner@example.com" {
		t.Fatalf("migration = %s", w.Body.String())
	}
	w = request("GET", "/api/models", "", true)
	if !strings.Contains(w.Body.String(), `"account_id":"`+list.Accounts[0].ID+`"`) {
		t.Fatalf("model binding absent: %s", w.Body.String())
	}
	w = request("PUT", "/api/models/0", `{"model_name":"renamed","model":"openai/gpt-5.4","auth_method":"oauth"}`, true)
	if w.Code != 200 {
		t.Fatalf("save model = %d %s", w.Code, w.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[1].APIKey != "api-secret" || cfg.ModelList[1].APIBase != "https://example.com/v1" ||
		cfg.ModelList[1].AuthMethod != "" {
		t.Fatalf("API configuration changed: %+v", cfg.ModelList[1])
	}
}

func TestCodexMissingCredentialsDoesNotInventConnection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	h, cookie, _ := modelInferenceDashboard(
		t,
		[]config.ModelConfig{{ModelName: "subscription", Model: "openai/gpt-5.4", AuthMethod: "oauth"}},
	)
	r := httptest.NewRequest("GET", "/api/oauth/accounts", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"accounts":[]`) {
		t.Fatalf("empty store: %d %s", w.Code, w.Body.String())
	}
}

func TestCodexSelectedModelUsesItsBoundConnection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := os.WriteFile(
		filepath.Join(os.Getenv(config.EnvHome), "auth.json"),
		[]byte(
			`{"accounts":{"first":{"id":"first","label":"First","provider":"openai","enabled":true,"credential":{"provider":"openai","auth_method":"oauth","access_token":"first-secret","account_id":"workspace-first"}},"second":{"id":"second","label":"Second","provider":"openai","enabled":true,"credential":{"provider":"openai","auth_method":"oauth","access_token":"second-secret","account_id":"workspace-second"}}}}`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var calls int
	oldTransport := http.DefaultTransport
	http.DefaultTransport = accountRecordingTransport{handler: func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer second-secret" ||
			r.Header.Get("Chatgpt-Account-Id") != "workspace-second" {
			t.Errorf(
				"wrong selected identity: %s / %s",
				r.Header.Get("Authorization"),
				r.Header.Get("Chatgpt-Account-Id"),
			)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"custom-model-preview"`) {
			t.Errorf("wrong model: %s", body)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(
				strings.NewReader(
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n",
				),
			),
			Request: r,
		}, nil
	}}
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var selected config.ModelConfig
	if err := json.Unmarshal(
		[]byte(
			`{"model_name":"selected","model":"openai/custom-model-preview","auth_method":"oauth","account_id":"second"}`,
		),
		&selected,
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{selected})
	r := httptest.NewRequest("POST", "/api/models/0/test", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || calls != 1 {
		t.Fatalf("selected probe: %d %s, calls=%d", w.Code, w.Body.String(), calls)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	w = dashboardRequest(h, "POST", "/api/models/by-id/"+cfg.ModelList[0].ID+"/test", "", cookie)
	if w.Code != 200 || calls != 2 {
		t.Fatalf("stable selected probe=%d %s calls%d", w.Code, w.Body.String(), calls)
	}
	w = dashboardRequest(
		h,
		"POST",
		"/api/models/test-inline",
		`{"id":"moved-entry","model_index":0,"model_name":"selected","model":"openai/custom-model-preview","auth_method":"oauth","account_id":"second"}`,
		cookie,
	)
	if w.Code != 409 || calls != 2 {
		t.Fatal("stale edit probe selected another model")
	}
}

type accountRecordingTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (t accountRecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.handler(r)
}

func TestCodexLoginKeepsAPIKeyModelsAndDefault(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{
				ModelName: "paid-api",
				Model:     "openai/gpt-5.4",
				APIKey:    "keep-secret",
				APIBase:   "https://custom.example/v1",
			},
		},
	)
	r := httptest.NewRequest(
		"POST",
		"/api/oauth/login",
		strings.NewReader(`{"provider":"openai","method":"token","token":"codex-secret"}`),
	)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[0].APIKey != "keep-secret" || cfg.ModelList[0].AuthMethod != "" ||
		cfg.ModelList[0].APIBase != "https://custom.example/v1" {
		t.Fatalf("login changed API-key configuration: %+v", cfg.ModelList[0])
	}
}

func TestModelRenamePreservesBindingAndUneditedSettings(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var selected config.ModelConfig
	if err := json.Unmarshal(
		[]byte(
			`{"model_name":"original","model":"openai/gpt-5.4","api_key":"saved-key","api_base":"https://custom.example/v1","fallbacks":["backup"],"request_timeout":97,"extra_body":{"frequency_penalty":0.3}}`,
		),
		&selected,
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{selected})
	r := httptest.NewRequest(
		"PUT",
		"/api/models/0",
		strings.NewReader(`{"model_name":"renamed","model":"openai/gpt-5.4"}`),
	)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	saved, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	model := saved.ModelList[0]
	if model.ModelName != "renamed" || model.APIKey != "saved-key" || model.APIBase != "https://custom.example/v1" ||
		model.RequestTimeout != 97 ||
		len(model.Fallbacks) != 1 ||
		model.Fallbacks[0] != "backup" ||
		model.ExtraBody["frequency_penalty"] != 0.3 {
		t.Fatalf("rename lost model settings: %+v", model)
	}
}

func TestCodexAccountPatchPreservesReferencesAndIndependentFields(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SaveAccount(
		&auth.AccountConnection{
			ID:         "first",
			Label:      "Same email",
			Provider:   "openai",
			Enabled:    true,
			Credential: &auth.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "first-secret"},
		},
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{{ModelName: "selected", Model: "openai/gpt-5.4", AuthMethod: "oauth", AccountID: "first"}},
	)
	if w := dashboardRequest(
		h,
		"PATCH",
		"/api/oauth/accounts/first",
		`{"label":"Renamed"}`,
		nil,
	); w.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected patch=%d", w.Code)
	}
	for _, body := range []string{`{"enabled":false}`, `{"label":"Renamed"}`} {
		w := dashboardRequest(h, "PATCH", "/api/oauth/accounts/first", body, cookie)
		if w.Code != 200 {
			t.Fatalf("patch=%d %s", w.Code, w.Body.String())
		}
	}
	a, err := auth.GetAccount("first")
	if err != nil {
		t.Fatal(err)
	}
	if a.Label != "Renamed" || a.Enabled || a.Credential.AccessToken != "first-secret" {
		t.Fatalf("patch lost independent field: %+v", a)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[0].AccountID != "first" {
		t.Fatal("rename changed stable model reference")
	}
	for _, body := range []string{`{"label":"  "}`, `{"enabled":true,"label":""}`, `{"access_token":"replace"}`, `{"enabled":true}{"enabled":false}`} {
		if w := dashboardRequest(h, "PATCH", "/api/oauth/accounts/first", body, cookie); w.Code != 400 {
			t.Fatalf("invalid patch accepted=%d %s", w.Code, w.Body.String())
		}
	}
	a, _ = auth.GetAccount("first")
	if a.Enabled {
		t.Fatal("partially applied invalid patch")
	}
}

func TestCodexExplicitAccountProbeRecordsOnlySelectedIdentity(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	for _, id := range []string{"first", "second"} {
		if err := auth.SaveAccount(
			&auth.AccountConnection{
				ID:       id,
				Label:    "Same email",
				Provider: "openai",
				Enabled:  true,
				Credential: &auth.AuthCredential{
					Provider:    "openai",
					AuthMethod:  "oauth",
					AccessToken: id + "-secret",
					AccountID:   "workspace-" + id,
				},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	calls, fail := 0, false
	original := http.DefaultTransport
	http.DefaultTransport = accountRecordingTransport{handler: func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer second-secret" ||
			r.Header.Get("Chatgpt-Account-Id") != "workspace-second" {
			t.Error("probe changed selected identity")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"gpt-5.4"`) {
			t.Errorf("probe changed model: %s", body)
		}
		status, content := 200, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n"
		if fail {
			status, content = 503, `{"error":{"code":"unavailable","message":"second-secret"}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(content)),
			Request:    r,
		}, nil
	}}
	t.Cleanup(func() { http.DefaultTransport = original })
	h, cookie, _ := modelInferenceDashboard(t, nil)
	list := func() string {
		t.Helper()
		w := dashboardRequest(h, "GET", "/api/oauth/accounts", "", cookie)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		return w.Body.String()
	}
	if result := list(); calls != 0 || strings.Contains(result, "last_probe") {
		t.Fatalf("view inferred availability: %s calls%d", result, calls)
	}
	if w := dashboardRequest(h, "POST", "/api/oauth/accounts/second/test", `{"model":"gpt-5.4"}`, nil); w.Code != 401 {
		t.Fatal("probe unprotected")
	}
	if w := dashboardRequest(
		h,
		"POST",
		"/api/oauth/accounts/second/test",
		`{"model":"gpt-5.4"}`,
		cookie,
	); w.Code != 200 {
		t.Fatalf("probe=%d %s", w.Code, w.Body.String())
	}
	successful := list()
	if calls != 1 || !strings.Contains(successful, `"last_success_at":`) ||
		!strings.Contains(successful, `"model":"gpt-5.4"`) ||
		strings.Contains(successful, "secret") {
		t.Fatalf("missing/sensitive successful probe: %s calls%d", successful, calls)
	}
	fail = true
	if w := dashboardRequest(
		h,
		"POST",
		"/api/oauth/accounts/second/test",
		`{"model":"gpt-5.4"}`,
		cookie,
	); w.Code != 502 ||
		strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("failure=%d %s", w.Code, w.Body.String())
	}
	failed := list()
	if calls != 2 || !strings.Contains(failed, `"status":"temporarily_unavailable"`) ||
		!strings.Contains(failed, `"last_success_at":`) {
		t.Fatalf("failed probe lost previous success or retried: %s calls%d", failed, calls)
	}
	if w := dashboardRequest(h, "PATCH", "/api/oauth/accounts/second", `{"enabled":false}`, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := dashboardRequest(
		h,
		"POST",
		"/api/oauth/accounts/second/test",
		`{"model":"gpt-5.4"}`,
		cookie,
	); w.Code != 400 ||
		calls != 2 {
		t.Fatalf("disabled account sent request: %d calls%d", w.Code, calls)
	}
}

func TestCodexAccountDeleteNamesReferencesAndRejectsLateBindings(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SaveAccount(
		&auth.AccountConnection{
			ID:         "selected",
			Label:      "Selected",
			Provider:   "openai",
			Enabled:    true,
			Credential: &auth.AuthCredential{AccessToken: "keep-secret"},
		},
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, _ := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ModelName: "Work model", Model: "openai/gpt-5.4", AuthMethod: "oauth", AccountID: "selected"},
		},
	)
	if w := dashboardRequest(h, "DELETE", "/api/oauth/accounts/selected", "", nil); w.Code != 401 {
		t.Fatal("delete unprotected")
	}
	w := dashboardRequest(h, "DELETE", "/api/oauth/accounts/selected", "", cookie)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"models":["Work model"]`) {
		t.Fatalf("delete ignored refs: %d %s", w.Code, w.Body.String())
	}
	if w := dashboardRequest(h, "PATCH", "/api/oauth/accounts/selected", `{"enabled":false}`, cookie); w.Code != 200 {
		t.Fatal("referenced account cannot disable")
	}
	if w := dashboardRequest(h, "DELETE", "/api/models/0", "", cookie); w.Code != 200 {
		t.Fatalf("unbind model=%d %s", w.Code, w.Body.String())
	}
	if w := dashboardRequest(h, "DELETE", "/api/oauth/accounts/selected", "", cookie); w.Code != 200 {
		t.Fatalf("unused delete=%d %s", w.Code, w.Body.String())
	}
	if _, err := auth.GetAccount("selected"); !errors.Is(err, auth.ErrAccountNotFound) {
		t.Fatal("account remained after deletion")
	}
	if w := dashboardRequest(
		h,
		"POST",
		"/api/models",
		`{"model_name":"Late binding","model":"openai/gpt-5.4","auth_method":"oauth","account_id":"selected"}`,
		cookie,
	); w.Code == 200 {
		t.Fatal("new model bound to deleted connection")
	}
}

func TestCodexDisableDuringRefreshKeepsRotationWithoutSendingInference(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	var releases sync.Once
	defer releases.Do(func() { close(release) })
	var refreshes, inference atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshes.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "issued-selected" || r.Form.Get("refresh_token") != "old-refresh" {
				t.Error("refresh changed selected registration")
			}
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(
				w,
				`{"access_token":"fresh-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`,
			)
		case "/v1/responses":
			inference.Add(1)
			if r.Header.Get("Authorization") != "Bearer fresh-access" {
				t.Error("reenabled connection used discarded session")
			}
			var request struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Model != "selected-model" {
				t.Error("reenabled probe switched model")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(
				w,
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"selected\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n",
			)
		default:
			t.Errorf("unexpected outbound path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer func() { releases.Do(func() { close(release) }); endpoint.Close() }()
	account := &auth.AccountConnection{
		ID:       "selected",
		Label:    "Selected",
		Provider: "openai",
		Enabled:  true,
		Subject:  "verified-subject",
		Issuer:   endpoint.URL,
		HostID:   "persistent-host",
		Resource: endpoint.URL + "/v1",
		Scopes:   auth.OpenAIPlanScope,
		OAuth: auth.OAuthProviderConfig{
			Issuer:   endpoint.URL,
			ClientID: "issued-selected",
			TokenURL: endpoint.URL + "/token",
			Resource: auth.OpenAIRegistrationResource,
		},
		Credential: &auth.AuthCredential{
			Provider:     "openai",
			AuthMethod:   "oauth",
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    time.Now().Add(-time.Hour),
		},
	}
	if err := auth.SaveAccount(account); err != nil {
		t.Fatal(err)
	}
	h, cookie, _ := modelInferenceDashboard(t, nil)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- dashboardRequest(h, "POST", "/api/oauth/accounts/selected/test", `{"model":"selected-model"}`, cookie)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start")
	}
	w := dashboardRequest(
		h,
		"PATCH",
		"/api/oauth/accounts/selected",
		`{"enabled":false,"label":"Disabled during refresh"}`,
		cookie,
	)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	releases.Do(func() { close(release) })
	select {
	case w = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not finish")
	}
	if w.Code != 502 || inference.Load() != 0 {
		t.Fatalf("disabled refresh sent inference: status%d calls%d", w.Code, inference.Load())
	}
	saved, err := auth.GetAccount("selected")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.Label != "Disabled during refresh" ||
		saved.Credential.RefreshToken != "rotated-refresh" ||
		saved.Credential.AccessToken != "fresh-access" {
		t.Fatal("disable discarded newest rotating credentials or metadata")
	}
	if saved.Subject != account.Subject || saved.OAuth.ClientID != account.OAuth.ClientID ||
		saved.SessionVersion != account.SessionVersion {
		t.Fatal("disable changed registration identity")
	}
	w = dashboardRequest(h, "PATCH", "/api/oauth/accounts/selected", `{"enabled":true}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = dashboardRequest(h, "POST", "/api/oauth/accounts/selected/test", `{"model":"selected-model"}`, cookie)
	if w.Code != 200 || inference.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf(
			"reenabled session did not resume: status%d inference%d refreshes%d %s",
			w.Code,
			inference.Load(),
			refreshes.Load(),
			w.Body.String(),
		)
	}
}
