package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAuthenticatedModelEditorPersistsInactivityPolicy(t *testing.T) {
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{{ModelName: "selected", Model: "openai/selected", APIKey: "preserved-key"}})
	for _, seconds := range []int{17, 0, -1} {
		w := dashboardRequest(h, "PUT", "/api/models/0", fmt.Sprintf(`{"model_name":"selected","model":"openai/selected","inactivity_timeout_seconds":%d}`, seconds), cookie)
		if seconds < 0 {
			if w.Code != 400 {
				t.Fatalf("negative timeout accepted: %d %s", w.Code, w.Body.String())
			}
			continue
		}
		if w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		w = dashboardRequest(h, "GET", "/api/models", "", cookie)
		var listed struct {
			Models []map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Models) != 1 {
			t.Fatalf("models: %s", w.Body.String())
		}
		var actual int
		raw, ok := listed.Models[0]["inactivity_timeout_seconds"]
		if !ok || json.Unmarshal(raw, &actual) != nil || actual != seconds {
			t.Fatalf("readback lost timeout: %s", w.Body.String())
		}
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		persisted, _ := json.Marshal(cfg.ModelList[0])
		var saved map[string]json.RawMessage
		_ = json.Unmarshal(persisted, &saved)
		actual = 0
		if raw = saved["inactivity_timeout_seconds"]; raw != nil {
			_ = json.Unmarshal(raw, &actual)
		}
		if actual != seconds || cfg.ModelList[0].APIKey != "preserved-key" {
			t.Fatalf("saved model=%s", persisted)
		}
	}
}
