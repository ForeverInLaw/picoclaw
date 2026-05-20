package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPProvider_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "bad auth: "+got, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "test-model" {
			http.Error(w, "wrong model", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3, 0.4}}},
		})
	}))
	defer srv.Close()

	p := &HTTPProvider{APIBase: srv.URL + "/v1", APIKey: "test-key", Model: "test-model"}
	vec, err := p.Embed(context.Background(), "", "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 4 {
		t.Fatalf("want 4 dims, got %d", len(vec))
	}
}

func TestHTTPProvider_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := &HTTPProvider{APIBase: srv.URL + "/v1", APIKey: "k", Model: "m"}
	_, err := p.Embed(context.Background(), "", "hello")
	if err == nil {
		t.Fatal("want error on 429")
	}
}

func TestHTTPProvider_NoBase(t *testing.T) {
	p := &HTTPProvider{}
	_, err := p.Embed(context.Background(), "", "hello")
	if err == nil {
		t.Fatal("want error when unconfigured")
	}
}
