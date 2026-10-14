package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestHubAndAgentSkillInstallShareWorkspaceMutation(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("SKILL.md")
	require.NoError(t, err)
	_, err = entry.Write([]byte("---\nname: shared\ndescription: Shared install\n---\nFixture\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	var downloads atomic.Int32
	started, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/download" {
			downloads.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(archive.Bytes())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": "shared", "latestVersion": map[string]string{"version": "1"},
			"moderation": map[string]bool{"isSuspicious": true},
		})
	}))
	defer registry.Close()
	defer finish()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.Skills.Registries.ClawHub.Enabled = true
	cfg.Tools.Skills.Registries.ClawHub.BaseURL = registry.URL
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, config.SaveConfig(path, cfg))
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	hubDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		mux.ServeHTTP(
			w,
			httptest.NewRequest(
				http.MethodPost,
				"/api/skills/hub/install",
				strings.NewReader(`{"slug":"shared","registry":"clawhub"}`),
			),
		)
		hubDone <- w
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Hub download did not start")
	}
	manager := skills.NewRegistryManagerFromConfig(
		skills.RegistryConfig{ClawHub: skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub)},
	)
	tool := tools.NewInstallSkillTool(manager, cfg.WorkspacePath())
	agentDone := make(chan *tools.ToolResult, 1)
	go func() {
		agentDone <- tool.Execute(t.Context(), map[string]any{"slug": "shared", "registry": "clawhub"})
	}()
	select {
	case <-started:
		t.Error("agent and Hub started simultaneous installs of the same workspace skill")
	case <-time.After(100 * time.Millisecond):
	}
	finish()
	hub := <-hubDone
	require.Equal(t, http.StatusCreated, hub.Code, hub.Body.String())
	agent := <-agentDone
	require.True(t, agent.IsError)
	require.Contains(t, agent.ForLLM, "already installed")
	require.Equal(t, int32(1), downloads.Load())
	installed := httptest.NewRecorder()
	mux.ServeHTTP(installed, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	require.Contains(t, installed.Body.String(), `"name":"shared"`)
	stale := filepath.Join(cfg.WorkspacePath(), "skills", "shared", "old.txt")
	require.NoError(t, os.WriteFile(stale, []byte("old version"), 0o600))
	forced := tool.Execute(
		t.Context(),
		map[string]any{"slug": "shared", "registry": "clawhub", "version": "2", "force": true},
	)
	require.False(t, forced.IsError, forced.ForLLM)
	require.True(t, forced.Silent)
	require.Contains(t, forced.ForLLM, "v2")
	require.Contains(t, forced.ForLLM, "suspicious")
	_, err = os.Stat(stale)
	require.True(t, os.IsNotExist(err), "force must replace the previous directory")
	require.Equal(t, int32(2), downloads.Load())
}
