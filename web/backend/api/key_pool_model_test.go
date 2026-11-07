package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestKeyPoolModelsComeFromAnthropicThroughTheNextWorkingKey(t *testing.T) {
	requests := fakeAnthropicModels(t, secondPoolSecret)
	mux := http.NewServeMux()
	NewHandler(keyPoolConfigPath(t)).RegisterRoutes(mux)

	rec := serve(mux, http.MethodGet, "/api/key-pools/orgs/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET pool models = %d %s", rec.Code, rec.Body.String())
	}
	assertNoPoolSecret(t, "GET pool models", rec.Body.String())
	var listing struct {
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Models) != 1 || listing.Models[0].ID != "claude-opus-5-5" ||
		listing.Models[0].DisplayName != "Claude Opus 5.5" {
		t.Fatalf("pool models = %+v", listing.Models)
	}
	want := []string{"GET /v1/models " + firstPoolSecret, "GET /v1/models " + secondPoolSecret}
	if !slices.Equal(*requests, want) {
		t.Fatalf("Anthropic received %q, want %q", *requests, want)
	}
}

func TestKeyPoolModelsFailWhenNoKeyCanListThem(t *testing.T) {
	fakeAnthropicModels(t, "sk-ant-api03-not-in-this-pool")
	mux := http.NewServeMux()
	NewHandler(keyPoolConfigPath(t)).RegisterRoutes(mux)

	rec := serve(mux, http.MethodGet, "/api/key-pools/orgs/models", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Fatalf("GET pool models with no working key = %d %s", rec.Code, rec.Body.String())
	}
	assertNoPoolSecret(t, "GET pool models", rec.Body.String())
	if rec := serve(mux, http.MethodGet, "/api/key-pools/missing/models", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET models of a missing pool = %d %s", rec.Code, rec.Body.String())
	}
}
