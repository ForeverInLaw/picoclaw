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
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func TestAuthenticatedSkillHubSearchDetailInstallAndInstalledList(t *testing.T) {
	const secret = "private-registry-token"
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: weather\ndescription: Weather fixture\n---\nUse the weather service.\n"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("registry token was not sent to registry")
		}
		switch r.URL.Path {
		case "/api/v1/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{
					"slug":        "weather",
					"displayName": "Weather",
					"summary":     "Forecast " + secret,
					"score":       3,
					"version":     "1.2",
				},
				{"slug": "calendar", "displayName": "Calendar", "summary": "Schedule", "score": 2, "version": "1.0"},
				{"slug": "notes", "displayName": "Notes", "summary": "Write notes", "score": 1, "version": "1.0"},
			}})
		case "/api/v1/skills/weather":
			_ = json.NewEncoder(w).
				Encode(map[string]any{"slug": "weather", "displayName": "Weather", "summary": "Forecast " + secret, "latestVersion": map[string]string{"version": "1.2"}})
		case "/api/v1/download":
			downloads++
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	cfg.Tools.Skills.Registries.ClawHub.Enabled = true
	cfg.Tools.Skills.Registries.ClawHub.BaseURL = registry.URL
	cfg.Tools.Skills.Registries.ClawHub.AuthToken = secret
	path := filepath.Join(t.TempDir(), "config.json")
	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	sessions := middleware.NewLauncherDashboardSessions()
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	h := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux)
	w := httptest.NewRecorder()
	if err = sessions.Issue(w, httptest.NewRequest(http.MethodGet, "/", nil), nil); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	request := func(method, route, body string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, route, strings.NewReader(body))
		if auth {
			r.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("registry secret leaked to browser: %s", response.Body.String())
		}
		return response
	}
	if w = request(http.MethodGet, "/api/skills/hub/search?q=weather", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous hub=%d", w.Code)
	}
	w = request(http.MethodGet, "/api/skills/hub/search?q=weather&limit=1&offset=0", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("search=%d %s", w.Code, w.Body.String())
	}
	var page struct {
		Results []struct {
			Slug string `json:"slug"`
		} `json:"results"`
		HasMore    bool `json:"has_more"`
		NextOffset int  `json:"next_offset"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Slug != "weather" || !page.HasMore || page.NextOffset != 1 {
		t.Fatalf("first page=%#v", page)
	}
	w = request(http.MethodGet, "/api/skills/hub/search?q=weather&limit=1&offset=1", "", true)
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Slug != "calendar" {
		t.Fatalf("second page=%#v", page)
	}
	w = request(http.MethodGet, "/api/skills/hub/clawhub/weather", "", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1.2") {
		t.Fatalf("detail=%d %s", w.Code, w.Body.String())
	}
	w = request(
		http.MethodPost,
		"/api/skills/hub/install",
		`{"slug":"weather","registry":"clawhub","version":"1.2"}`,
		true,
	)
	if w.Code != http.StatusCreated {
		t.Fatalf("install=%d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodGet, "/api/skills", "", true)
	if !strings.Contains(w.Body.String(), `"name":"weather"`) {
		t.Fatalf("installed list=%s", w.Body.String())
	}
	w = request(http.MethodPost, "/api/skills/hub/install", `{"slug":"weather","registry":"clawhub"}`, true)
	if w.Code != http.StatusConflict || downloads != 1 {
		t.Fatalf("duplicate=%d downloads=%d", w.Code, downloads)
	}
	w = request(http.MethodDelete, "/api/skills/weather", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("existing delete=%d", w.Code)
	}
}
