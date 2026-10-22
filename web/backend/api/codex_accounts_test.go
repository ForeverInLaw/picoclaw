package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCodexLegacyConnectionMigratesWithoutChangingAPIKeyModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"credentials":{"openai":{"access_token":"legacy-secret","refresh_token":"refresh-secret","account_id":"workspace-1","provider":"openai","auth_method":"oauth","email":"owner@example.com"}}}`), 0600); err != nil {
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
	if cfg.ModelList[1].APIKey != "api-secret" || cfg.ModelList[1].APIBase != "https://example.com/v1" || cfg.ModelList[1].AuthMethod != "" {
		t.Fatalf("API configuration changed: %+v", cfg.ModelList[1])
	}
}

func TestCodexMissingCredentialsDoesNotInventConnection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	h, cookie, _ := modelInferenceDashboard(t, []config.ModelConfig{{ModelName: "subscription", Model: "openai/gpt-5.4", AuthMethod: "oauth"}})
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
	if err := os.WriteFile(filepath.Join(os.Getenv(config.EnvHome), "auth.json"), []byte(`{"accounts":{"first":{"id":"first","label":"First","provider":"openai","enabled":true,"credential":{"provider":"openai","auth_method":"oauth","access_token":"first-secret","account_id":"workspace-first"}},"second":{"id":"second","label":"Second","provider":"openai","enabled":true,"credential":{"provider":"openai","auth_method":"oauth","access_token":"second-secret","account_id":"workspace-second"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls int
	oldTransport := http.DefaultTransport
	http.DefaultTransport = accountRecordingTransport{handler: func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer second-secret" || r.Header.Get("Chatgpt-Account-Id") != "workspace-second" {
			t.Errorf("wrong selected identity: %s / %s", r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"gpt-5.4"`) {
			t.Errorf("wrong model: %s", body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")), Request: r}, nil
	}}
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var selected config.ModelConfig
	if err := json.Unmarshal([]byte(`{"model_name":"selected","model":"openai/gpt-5.4","auth_method":"oauth","account_id":"second"}`), &selected); err != nil {
		t.Fatal(err)
	}
	h, cookie, _ := modelInferenceDashboard(t, []config.ModelConfig{selected})
	r := httptest.NewRequest("POST", "/api/models/0/test", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || calls != 1 {
		t.Fatalf("selected probe: %d %s, calls=%d", w.Code, w.Body.String(), calls)
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
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{{ModelName: "paid-api", Model: "openai/gpt-5.4", APIKey: "keep-secret", APIBase: "https://custom.example/v1"}})
	r := httptest.NewRequest("POST", "/api/oauth/login", strings.NewReader(`{"provider":"openai","method":"token","token":"codex-secret"}`))
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
	if cfg.ModelList[0].APIKey != "keep-secret" || cfg.ModelList[0].AuthMethod != "" || cfg.ModelList[0].APIBase != "https://custom.example/v1" {
		t.Fatalf("login changed API-key configuration: %+v", cfg.ModelList[0])
	}
}

func TestModelRenamePreservesBindingAndUneditedSettings(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var selected config.ModelConfig
	if err := json.Unmarshal([]byte(`{"model_name":"original","model":"openai/gpt-5.4","api_key":"saved-key","api_base":"https://custom.example/v1","fallbacks":["backup"],"request_timeout":97,"extra_body":{"frequency_penalty":0.3}}`), &selected); err != nil {
		t.Fatal(err)
	}
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{selected})
	r := httptest.NewRequest("PUT", "/api/models/0", strings.NewReader(`{"model_name":"renamed","model":"openai/gpt-5.4"}`))
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
	if model.ModelName != "renamed" || model.APIKey != "saved-key" || model.APIBase != "https://custom.example/v1" || model.RequestTimeout != 97 || len(model.Fallbacks) != 1 || model.Fallbacks[0] != "backup" || model.ExtraBody["frequency_penalty"] != 0.3 {
		t.Fatalf("rename lost model settings: %+v", model)
	}
}
