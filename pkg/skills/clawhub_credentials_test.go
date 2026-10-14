package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryErrorsDoNotExposeCredentialsToCallers(t *testing.T) {
	const token = "registry-private-token"
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Rejected Authorization: " + r.Header.Get("Authorization")))
	}))
	defer registry.Close()
	client := NewClawHubRegistry(ClawHubConfig{BaseURL: registry.URL, AuthToken: token})
	_, err := client.Search(t.Context(), "search", 1)
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("public error=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Search(ctx, "search", 1)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestRegistryRedirectDoesNotForwardTokenToAnotherEndpoint(t *testing.T) {
	var forwarded string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer destination.Close()
	registry := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) },
		),
	)
	defer registry.Close()
	client := NewClawHubRegistry(ClawHubConfig{BaseURL: registry.URL, AuthToken: "private-token"})
	if _, err := client.Search(t.Context(), "search", 1); err != nil {
		t.Fatal(err)
	}
	if forwarded != "" {
		t.Fatal("registry token was forwarded to another endpoint")
	}
}
