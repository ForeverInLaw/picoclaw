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
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 2 || list.Models[0].ID == "" || list.Models[1].ID == "" || list.Models[0].ID == list.Models[1].ID {
		t.Fatalf("distinct entry IDs missing: %s", w.Body.String())
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "limited"}, {ID: "inherited"}}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	primary := "model-id:" + list.Models[1].ID
	reserve := "model-id:" + list.Models[0].ID
	body := `{"defaults":{"primary":"` + primary + `","fallbacks":["` + reserve + `"]},"agents":{"limited":{"primary":"` + primary + `","fallbacks":[]},"inherited":null}}`
	if w = call("PUT", "/api/models/routing", body, true); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w = call("PUT", "/api/models/1", `{"model_name":"renamed","model":"openai/shared","api_key":"rotated-secret"}`, true); w.Code != 200 {
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
	if cfg.Agents.List[0].Model == nil || cfg.Agents.List[0].Model.Fallbacks == nil || len(cfg.Agents.List[0].Model.Fallbacks) != 0 || cfg.Agents.List[1].Model != nil {
		t.Fatalf("override inheritance lost: %+v", cfg.Agents.List)
	}
	if w = call("DELETE", "/api/models/1", "", true); w.Code != http.StatusConflict {
		t.Fatalf("referenced entry deletion: %d %s", w.Code, w.Body.String())
	}
	if w = call("PUT", "/api/models/routing", `{"defaults":{"primary":"model-id:missing","fallbacks":[]},"agents":{}}`, true); w.Code != 400 {
		t.Fatalf("dangling reference accepted: %d %s", w.Code, w.Body.String())
	}
}
