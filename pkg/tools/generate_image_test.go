package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

func TestGenerateImageTool_UsesDefaultImageModelAndStoresMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	var gotAuth string
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"created": 1,
			"data": []map[string]any{
				{
					"b64_json":       tinyPNGBase64,
					"revised_prompt": "revised prompt",
				},
			},
		})
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ImageGenerationModel = "img-default"
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "img-default",
			Model:     "openai/gpt-image-1",
			APIBase:   server.URL + "/v1",
			APIKeys:   config.SimpleSecureStrings("sk-image"),
		},
	}

	tool := NewGenerateImageTool(func() *config.Config { return cfg })
	tool.SetMediaStore(store)

	ctx := WithToolContext(context.Background(), "telegram", "chat42")
	result := tool.Execute(ctx, map[string]any{
		"prompt":        "a cat on the moon",
		"quality":       "high",
		"output_format": "png",
	})

	if result.IsError {
		t.Fatalf("Execute() unexpected error: %s", result.ForLLM)
	}
	if !result.ResponseHandled {
		t.Fatal("expected ResponseHandled to be true")
	}
	if len(result.Media) != 1 {
		t.Fatalf("len(result.Media) = %d, want 1", len(result.Media))
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("request path = %q, want %q", gotPath, "/v1/images/generations")
	}
	if gotAuth != "Bearer sk-image" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer sk-image")
	}
	if gotBody["model"] != "gpt-image-1" {
		t.Fatalf("model = %#v, want %q", gotBody["model"], "gpt-image-1")
	}
	if gotBody["prompt"] != "a cat on the moon" {
		t.Fatalf("prompt = %#v, want %q", gotBody["prompt"], "a cat on the moon")
	}
	if gotBody["response_format"] != "b64_json" {
		t.Fatalf("response_format = %#v, want %q", gotBody["response_format"], "b64_json")
	}

	path, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("ResolveWithMeta() error = %v", err)
	}
	defer os.Remove(path)

	if !strings.HasPrefix(meta.ContentType, "image/") {
		t.Fatalf("content type = %q, want image/*", meta.ContentType)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generated file stat error: %v", err)
	}
	if !strings.Contains(result.ForLLM, "revised prompt") {
		t.Fatalf("ForLLM should mention revised prompt, got %q", result.ForLLM)
	}
}

func TestGenerateImageTool_ErrorsWithoutDefaultImageModel(t *testing.T) {
	cfg := config.DefaultConfig()
	tool := NewGenerateImageTool(func() *config.Config { return cfg })
	tool.SetMediaStore(media.NewFileMediaStore())

	ctx := WithToolContext(context.Background(), "telegram", "chat42")
	result := tool.Execute(ctx, map[string]any{"prompt": "test"})

	if !result.IsError {
		t.Fatal("expected error when no default image-generation model is configured")
	}
	if !strings.Contains(result.ForLLM, "no default image-generation model configured") {
		t.Fatalf("unexpected error: %q", result.ForLLM)
	}
}
