package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRegistryCredentialsStayMaskedAndBoundToTheirEndpoint(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Skills.Registries.ClawHub.AuthToken = "private-registry-token"
	cfg.Tools.Skills.Registries.ClawHub.BaseURL = "https://old-registry.example"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if strings.Contains(get.Body.String(), "private-registry-token") {
		t.Fatal("config GET exposed registry credential")
	}
	put := httptest.NewRecorder()
	mux.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(get.Body.String())))
	if put.Code != http.StatusOK {
		t.Fatalf("unchanged save=%d %s", put.Code, put.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Tools.Skills.Registries.ClawHub.AuthToken != "private-registry-token" {
		t.Fatal("masked save did not preserve stored token", err)
	}
	patch := httptest.NewRecorder()
	mux.ServeHTTP(
		patch,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/config",
			strings.NewReader(
				`{"tools":{"skills":{"registries":{"clawhub":{"base_url":"https://new-registry.example"}}}}}`,
			),
		),
	)
	if patch.Code != http.StatusOK {
		t.Fatalf("endpoint update=%d %s", patch.Code, patch.Body.String())
	}
	loaded, err = config.LoadConfig(path)
	if err != nil || loaded.Tools.Skills.Registries.ClawHub.AuthToken != "" {
		t.Fatal("old registry token moved to another endpoint", err)
	}
	patch = httptest.NewRecorder()
	mux.ServeHTTP(
		patch,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/config",
			strings.NewReader(
				`{"tools":{"skills":{"registries":{"clawhub":{"auth_token":"replacement-registry-token"}}}}}`,
			),
		),
	)
	loaded, err = config.LoadConfig(path)
	if patch.Code != http.StatusOK || err != nil ||
		loaded.Tools.Skills.Registries.ClawHub.AuthToken != "replacement-registry-token" {
		t.Fatal("explicit replacement was not persisted", err)
	}
}
