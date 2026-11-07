package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	firstPoolSecret  = "sk-ant-api03-first-organization-1111"
	secondPoolSecret = "sk-ant-api03-second-organization-2222"
)

func keyPoolConfigPath(t *testing.T) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Anthropic organizations", Keys: []config.KeyPoolKey{
		{ID: "first", Label: "First org", Enabled: true, APIKey: firstPoolSecret},
		{ID: "second", Label: "Second org", Enabled: true, APIKey: secondPoolSecret},
	}}}
	cfg.ModelList = []config.ModelConfig{{ModelName: "claude", Model: "anthropic/claude-opus-5-5", KeyPoolID: "orgs"}}
	cfg.Agents.Defaults.ModelName = "claude"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func serve(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func assertNoPoolSecret(t *testing.T, listing, body string) {
	t.Helper()
	if strings.Contains(body, firstPoolSecret) || strings.Contains(body, secondPoolSecret) {
		t.Fatalf("%s exposed a Key pool secret: %s", listing, body)
	}
}

func TestKeyPoolSecretsStayMaskedInConfigurationListings(t *testing.T) {
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	models := serve(mux, http.MethodGet, "/api/models", "")
	assertNoPoolSecret(t, "GET /api/models", models.Body.String())
	if !strings.Contains(models.Body.String(), `"key_pool_id":"orgs"`) ||
		!strings.Contains(models.Body.String(), `"configured":true`) {
		t.Errorf("GET /api/models does not show the configured pool reference: %s", models.Body.String())
	}
	get := serve(mux, http.MethodGet, "/api/config", "")
	assertNoPoolSecret(t, "GET /api/config", get.Body.String())

	if put := serve(mux, http.MethodPut, "/api/config", get.Body.String()); put.Code != http.StatusOK {
		t.Fatalf("saving the listed config = %d %s", put.Code, put.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if keys := loaded.KeyPools[0].Keys; keys[0].APIKey != firstPoolSecret || keys[1].APIKey != secondPoolSecret {
		t.Fatalf("saving masked keys replaced the stored secrets: %+v", keys)
	}

	replaced := strings.Replace(get.Body.String(), maskAPIKey(secondPoolSecret), "sk-ant-api03-replacement", 1)
	if put := serve(mux, http.MethodPut, "/api/config", replaced); put.Code != http.StatusOK {
		t.Fatalf("replacing a key = %d %s", put.Code, put.Body.String())
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if keys := loaded.KeyPools[0].Keys; keys[0].APIKey != firstPoolSecret || keys[1].APIKey != "sk-ant-api03-replacement" {
		t.Fatalf("explicit replacement was not saved alone: %+v", keys)
	}
}
