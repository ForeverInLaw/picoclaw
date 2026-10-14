package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSkillHubKeepsRejectedArchivesOutOfInstalledList(t *testing.T) {
	for _, test := range []struct {
		name    string
		entry   string
		blocked bool
		want    int
	}{
		{"malware", "SKILL.md", true, http.StatusForbidden},
		{"traversal", "../escaped.md", false, http.StatusBadGateway},
		{"invalid-skill", "notes.txt", false, http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			entry, err := writer.Create(test.entry)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = entry.Write([]byte("---\nname: rejected\ndescription: Test\n---\nBody\n"))
			_ = writer.Close()
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/download" {
					_, _ = w.Write(archive.Bytes())
					return
				}
				_ = json.NewEncoder(w).
					Encode(map[string]any{"slug": "rejected", "latestVersion": map[string]string{"version": "1"}, "moderation": map[string]bool{"isMalwareBlocked": test.blocked}})
			}))
			defer registry.Close()
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
			cfg.Tools.Skills.Registries.ClawHub.Enabled = true
			cfg.Tools.Skills.Registries.ClawHub.BaseURL = registry.URL
			path := filepath.Join(t.TempDir(), "config.json")
			if err = config.SaveConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			NewHandler(path).RegisterRoutes(mux)
			w := httptest.NewRecorder()
			mux.ServeHTTP(
				w,
				httptest.NewRequest(
					http.MethodPost,
					"/api/skills/hub/install",
					strings.NewReader(`{"slug":"rejected","registry":"clawhub"}`),
				),
			)
			if w.Code != test.want {
				t.Fatalf("install=%d %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
			if strings.Contains(w.Body.String(), `"name":"rejected"`) {
				t.Fatal("rejected skill became installed")
			}
		})
	}
}
