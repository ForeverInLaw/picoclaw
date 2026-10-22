package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestRuntimeStatusReportsActualCapturedModelWithoutRequestData(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer FIRST_SECRET" {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"PRIVATE_ERROR with FIRST_SECRET"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer SECOND_SECRET" {
			t.Error("wrong account identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"selected\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PRIVATE_ANSWER\"}]}]}}\n\n",
		)
	}))
	defer endpoint.Close()
	for _, saved := range []struct{ id, token string }{{"first", "FIRST_SECRET"}, {"second", "SECOND_SECRET"}} {
		if err := auth.SaveAccount(
			&auth.AccountConnection{
				ID:         saved.id,
				Label:      "Same label",
				Provider:   "openai",
				Enabled:    true,
				Resource:   endpoint.URL + "/v1",
				Scopes:     auth.OpenAIPlanScope,
				Credential: &auth.AuthCredential{AccessToken: saved.token, Provider: "openai", AuthMethod: "oauth"},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ID: "model-first", ModelName: "First", Model: "openai/selected", AuthMethod: "oauth", AccountID: "first"},
			{
				ID:         "model-second",
				ModelName:  "Second",
				Model:      "openai/selected",
				AuthMethod: "oauth",
				AccountID:  "second",
			},
		},
	)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "model-first"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + "model-second"}
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	defer mb.Close()
	loop := agent.NewAgentLoop(cfg, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	defer loop.Close()
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer status.Close()
	answer, err := loop.ProcessDirect(t.Context(), "PRIVATE_PROMPT", "PRIVATE_CHAT_ID")
	if err != nil || answer != "PRIVATE_ANSWER" {
		t.Fatalf("request=%q %v", answer, err)
	}
	if err := status.Flush(); err != nil {
		t.Fatal(err)
	}
	previous := gatewayHealthGet
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"pid":%d,"status":"ok"}`, os.Getpid()))),
		}, nil
	}
	defer func() { gatewayHealthGet = previous }()
	if w := dashboardRequest(h, "GET", "/api/models/runtime", "", nil); w.Code != 401 {
		t.Fatal("runtime status is public")
	}
	w := dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	var actual struct {
		State   string `json:"state"`
		Runtime struct {
			Records []struct {
				State    string `json:"state"`
				Selected struct {
					ID        string `json:"id"`
					AccountID string `json:"account_id"`
				} `json:"selected"`
				Chain []struct {
					ID string `json:"id"`
				} `json:"chain"`
				Attempts []struct {
					Reason string `json:"reason"`
				} `json:"attempts"`
			} `json:"records"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.State != "running" || len(actual.Runtime.Records) != 1 {
		t.Fatalf("missing actual runtime: %s", w.Body.String())
	}
	record := actual.Runtime.Records[0]
	if record.State != "completed" || record.Selected.ID != "model-second" || record.Selected.AccountID != "second" ||
		len(record.Chain) != 2 ||
		record.Chain[0].ID != "model-first" {
		t.Fatalf("wrong served/captured identity: %s", w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("status sent inference: %d", calls.Load())
	}
	for _, secret := range []string{"FIRST_SECRET", "SECOND_SECRET", "PRIVATE_ERROR", "PRIVATE_PROMPT", "PRIVATE_ANSWER", "PRIVATE_CHAT_ID", "config_key", "access_token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("status leaked %s", secret)
		}
	}
}
