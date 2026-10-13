package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDashboardV3ConfigReadsAndEditsNamedInstances(t *testing.T) {
	path, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg := config.DefaultConfig()
	cfg.ChannelList = config.ChannelList{
		"work": {Type: "telegram", Settings: json.RawMessage(`{"token":"named-secret"}`)},
	}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(path)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/config",
		bytes.NewBufferString(`{"channels":{"work":{"enabled":true,"token":"changed-secret"}}}`),
	)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", recorder.Code, recorder.Body.String())
	}
	updated, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := updated.ChannelConfig("work")
	if err != nil || !instance.Channels.Telegram.Enabled || instance.Channels.Telegram.Token != "changed-secret" {
		t.Fatalf("named edit lost: %#v %v", instance, err)
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var response map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["channel_list"] == nil || response["channels"] != nil || string(response["version"]) != "3" {
		t.Fatalf("dashboard config is not v3: %s", recorder.Body.String())
	}
}
