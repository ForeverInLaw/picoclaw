package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestAuthenticatedModelEditorPersistsInactivityPolicy(t *testing.T) {
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{
				ModelName: "selected",
				Model:     "openai/selected",
				APIKey:    "preserved-key",
				APIKeys:   []string{"reserve-key"},
			},
		},
	)
	for _, seconds := range []int{17, 0, -1} {
		w := dashboardRequest(
			h,
			"PUT",
			"/api/models/0",
			fmt.Sprintf(`{"model_name":"selected","model":"openai/selected","inactivity_timeout_seconds":%d}`, seconds),
			cookie,
		)
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
		if len(cfg.ModelList) != 2 {
			t.Fatalf("multi-key model lost runtime entries: %d", len(cfg.ModelList))
		}
		for _, model := range cfg.ModelList {
			if model.InactivityTimeoutSeconds != seconds {
				t.Fatalf("runtime key slot lost wait policy: %+v", model)
			}
		}
		persisted, _ := json.Marshal(cfg.ConfiguredModels()[0])
		var saved map[string]json.RawMessage
		_ = json.Unmarshal(persisted, &saved)
		actual = 0
		if raw = saved["inactivity_timeout_seconds"]; raw != nil {
			_ = json.Unmarshal(raw, &actual)
		}
		if actual != seconds || cfg.ConfiguredModels()[0].APIKey != "preserved-key" {
			t.Fatalf("saved model=%s", persisted)
		}
	}
}

type inactivityProbeTransport func(*http.Request) (*http.Response, error)

func (f inactivityProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSelectedModelProbeHonorsConfiguredInactivityWithoutFallback(t *testing.T) {
	h, cookie, _ := modelInferenceDashboard(t, []config.ModelConfig{
		{
			ModelName:                "selected",
			Model:                    "openai/selected",
			APIBase:                  "https://model.test",
			APIKey:                   "selected-key",
			InactivityTimeoutSeconds: 1,
		},
		{ModelName: "reserve", Model: "openai/reserve", APIBase: "https://model.test", APIKey: "reserve-key"},
	})
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := 0
		http.DefaultTransport = inactivityProbeTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Header.Get("Authorization") != "Bearer selected-key" {
				t.Error("probe borrowed another model")
			}
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		if w := dashboardRequest(h, "GET", "/api/models", "", cookie); w.Code != 200 || calls != 0 {
			t.Fatalf("view performed inference: status=%d calls=%d", w.Code, calls)
		}
		start := time.Now()
		w := dashboardRequest(h, "POST", "/api/models/0/test", "", cookie)
		if w.Code != 502 || !strings.Contains(w.Body.String(), "timed out") || calls != 1 ||
			time.Since(start) != time.Second {
			t.Fatalf("status=%d body=%s calls=%d elapsed=%v", w.Code, w.Body.String(), calls, time.Since(start))
		}
	})
}

func TestSelectedAccountProbeBoundsCredentialAcquisition(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	if err := auth.SaveAccount(
		&auth.AccountConnection{
			ID:         "selected",
			Provider:   "openai",
			Enabled:    true,
			Credential: &auth.AuthCredential{AccessToken: "fixture", AuthMethod: "oauth"},
		},
	); err != nil {
		t.Fatal(err)
	}
	h, cookie, _ := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{
				ModelName:                "selected",
				Model:                    "openai/o1",
				AuthMethod:               "oauth",
				AccountID:                "selected",
				InactivityTimeoutSeconds: 1,
			},
		},
	)
	synctest.Test(t, func(t *testing.T) {
		lock, err := fileutil.LockFile(t.Context(), filepath.Join(home, "auth.json.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		released := make(chan struct{})
		defer func() { <-released }()
		go func() { time.Sleep(3 * time.Second); _ = lock.Close(); close(released) }()
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := 0
		http.DefaultTransport = inactivityProbeTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Request:    r,
				Body: io.NopCloser(
					strings.NewReader(
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"probe\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n",
					),
				),
			}, nil
		})
		started := time.Now()
		w := dashboardRequest(h, "POST", "/api/models/0/test", "", cookie)
		if w.Code != 502 || !strings.Contains(w.Body.String(), "timed out") || calls != 0 ||
			time.Since(started) != time.Second {
			t.Fatalf("status=%d body=%s calls=%d elapsed=%v", w.Code, w.Body.String(), calls, time.Since(started))
		}
	})
}
