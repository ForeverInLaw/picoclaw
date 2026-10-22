package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
