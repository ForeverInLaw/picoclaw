package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRoutingAPISelectsDistinctConfiguredEntriesAndPreservesEmptyOverrides(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{
		{ModelName: "same", Model: "openai/shared", APIBase: "https://first.example/v1", APIKey: "first-secret"},
		{ModelName: "same", Model: "openai/shared", APIBase: "https://second.example/v1", APIKey: "second-secret"},
	})
	call := func(method, url, body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		if authorized {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/models/routing", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected routing: %d", w.Code)
	}
	w := call("GET", "/api/models", "", true)
	var list struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 2 || list.Models[0].ID == "" || list.Models[1].ID == "" ||
		list.Models[0].ID == list.Models[1].ID {
		t.Fatalf("distinct entry IDs missing: %s", w.Body.String())
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "limited"}, {ID: "inherited"}}
	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	primary := "model-id:" + list.Models[1].ID
	reserve := "model-id:" + list.Models[0].ID
	body := `{"defaults":{"primary":"` + primary + `","fallbacks":["` + reserve + `"]},"agents":{"limited":{"primary":"` + primary + `","fallbacks":[]},"inherited":null}}`
	if w = call("PUT", "/api/models/routing", body, true); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w = call(
		"PUT",
		"/api/models/1",
		`{"model_name":"renamed","model":"openai/shared","api_key":"rotated-secret"}`,
		true,
	); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	cfg, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := cfg.GetModelConfig(primary)
	if err != nil || mc.ModelName != "renamed" || mc.APIKey != "rotated-secret" {
		t.Fatalf("durable selection lost: %+v %v", mc, err)
	}
	if cfg.Agents.List[0].Model == nil || cfg.Agents.List[0].Model.Fallbacks == nil ||
		len(cfg.Agents.List[0].Model.Fallbacks) != 0 ||
		cfg.Agents.List[1].Model != nil {
		t.Fatalf("override inheritance lost: %+v", cfg.Agents.List)
	}
	if w = call("DELETE", "/api/models/1", "", true); w.Code != http.StatusConflict {
		t.Fatalf("referenced entry deletion: %d %s", w.Code, w.Body.String())
	}
	if w = call(
		"PUT",
		"/api/models/routing",
		`{"defaults":{"primary":"model-id:missing","fallbacks":[]},"agents":{}}`,
		true,
	); w.Code != 400 {
		t.Fatalf("dangling reference accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestRoutingScopedSaveAndStableEditPreserveOtherSettingsAndMultiKeyModels(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{
		{
			ModelName:    "multi",
			Model:        "openai/shared",
			APIBase:      "https://example.com/v1",
			APIKeys:      []string{"first-secret", "second-secret"},
			Fallbacks:    []string{},
			ExtraBody:    map[string]any{"custom": "retained"},
			Capabilities: &config.ModelCapabilities{ContextWindow: 64000},
		},
		{ModelName: "other", Model: "openai/shared", APIBase: "https://other.example/v1", APIKey: "other-secret"},
	})
	call := func(method, url, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	original := cfg.ConfiguredModels()
	if len(original) != 2 {
		t.Fatalf("configured models = %d", len(original))
	}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "worker"},
		{ID: "untouched", Model: &config.AgentModelConfig{Primary: "other", Fallbacks: []string{}}},
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + original[0].ID
	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	w := call("PUT", "/api/models/routing", `{"agents":{"worker":{"primary":"other","fallbacks":[]}}}`)
	if w.Code != 200 {
		t.Fatalf("scoped save: %d %s", w.Code, w.Body.String())
	}
	cfg, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Defaults.GetModelName() != config.ModelIDPrefix+original[0].ID || cfg.Agents.List[1].Model == nil ||
		cfg.Agents.List[1].Model.Fallbacks == nil {
		t.Fatalf("scoped save overwrote settings: %+v", cfg.Agents)
	}
	cfg.ModelList = cfg.ConfiguredModels()
	cfg.ModelList[0], cfg.ModelList[1] = cfg.ModelList[1], cfg.ModelList[0]
	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	w = call("PUT", "/api/models/by-id/"+original[0].ID, `{"model_name":"multi-renamed","model":"openai/shared"}`)
	if w.Code != 200 {
		t.Fatalf("stable edit: %d %s", w.Code, w.Body.String())
	}
	cfg, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := cfg.ConfiguredModels()
	if len(saved) != 2 || saved[1].ID != original[0].ID || saved[1].ModelName != "multi-renamed" ||
		len(saved[1].APIKeys) != 2 ||
		saved[1].ExtraBody["custom"] != "retained" ||
		saved[1].Capabilities.ContextWindow != 64000 {
		t.Fatalf("save flattened/rebound multi-key entry: %+v", saved)
	}
	w = call("PUT", "/api/models/by-id/"+original[0].ID, `{"id":"changed","model_name":"bad","model":"openai/shared"}`)
	if w.Code != 400 {
		t.Fatalf("ID replacement accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestProtectedGatewayReadinessUsesOnlyConfiguredReserveChain(t *testing.T) {
	for _, mode := range []string{"disabled", "reauthorization"} {
		t.Run(mode, func(t *testing.T) {
			resetGatewayTestState(t)
			resetModelProbeHooks(t)
			t.Setenv(config.EnvHome, t.TempDir())
			account := &auth.AccountConnection{ID: "unavailable", Provider: "openai", Enabled: mode != "disabled"}
			if mode == "disabled" {
				account.Credential = &auth.AuthCredential{
					Provider:    "openai",
					AuthMethod:  "oauth",
					AccessToken: "unused-token",
				}
			}
			if err := auth.SaveAccount(account); err != nil {
				t.Fatal(err)
			}
			h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{
				{ModelName: "primary", Model: "openai/shared", AuthMethod: "oauth", AccountID: "unavailable"},
				{
					ModelName: "reserve",
					Model:     "openai/shared",
					APIBase:   "https://no-probe.invalid/v1",
					APIKey:    "reserve-secret",
				},
				{ModelName: "outside-chain", Model: "openai/unrelated", APIKey: "unrelated-secret"},
			})
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + cfg.ModelList[0].ID
			cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + cfg.ModelList[1].ID}
			if err = config.SaveConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			gatewayHealthGet = func(string, time.Duration) (*http.Response, error) { return nil, errors.New("offline") }
			probes := 0
			probeOpenAICompatibleModelFunc = func(string, string, string) bool { probes++; return false }
			probeTCPServiceFunc = func(string) bool { probes++; return false }
			status := func(authorized bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/api/gateway/status", nil)
				if authorized {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := status(false); w.Code != 401 {
				t.Fatalf("unprotected status: %d", w.Code)
			}
			w := status(true)
			if w.Code != 200 {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			var result struct {
				Allowed bool `json:"gateway_start_allowed"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Allowed {
				t.Fatalf("configured reserve blocked: %s", w.Body.String())
			}
			cfg.Agents.Defaults.ModelFallbacks = []string{}
			if err = config.SaveConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			w = status(true)
			if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Allowed {
				t.Fatalf("non-chain model enabled startup: %s", w.Body.String())
			}
			if probes != 0 {
				t.Fatalf("remote readiness probed inference: %d", probes)
			}
		})
	}
}
