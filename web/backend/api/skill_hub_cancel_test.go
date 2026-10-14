package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSkillHubCanceledDownloadNeverActivatesSkill(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/download" {
			close(started)
			<-r.Context().Done()
			close(stopped)
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"slug": "cancelled", "latestVersion": map[string]string{"version": "1"}})
	}))
	defer registry.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	cfg.Tools.Skills.Registries.ClawHub.Enabled = true
	cfg.Tools.Skills.Registries.ClawHub.BaseURL = registry.URL
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/api/skills/hub/install",
		strings.NewReader(`{"slug":"cancelled","registry":"clawhub"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, requestErr := http.DefaultClient.Do(req)
		if res != nil {
			_ = res.Body.Close()
		}
		done <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("registry request was not canceled")
	}
	res, err := http.Get(server.URL + "/api/skills")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil || strings.Contains(string(data), `"name":"cancelled"`) {
		t.Fatal("canceled skill activated", err)
	}
}
