package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadImageCapabilityConfig(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func acceptsImagesByName(cfg *Config) map[string]bool {
	accepts := map[string]bool{}
	for i := range cfg.ModelList {
		accepts[cfg.ModelList[i].ModelName] = cfg.ModelList[i].AcceptsImages()
	}
	return accepts
}

func TestVersion3ConfigGetsExplicitImageCapabilities(t *testing.T) {
	cfg := loadImageCapabilityConfig(t, `{
		"version": 3,
		"agents": {"defaults": {"model_name": "haiku", "image_model": "model-id:glm-id"}},
		"model_list": [
			{"model_name": "haiku", "model": "anthropic/claude-haiku-5-5", "api_key": "k"},
			{"model_name": "luna", "model": "openai/gpt-6-luna", "auth_method": "oauth"},
			{"model_name": "gpt", "model": "openai/gpt-5", "api_key": "k"},
			{"id": "glm-id", "model_name": "glm", "model": "openai/zai-org/GLM", "api_base": "https://inference.example/v1", "api_key": "k"},
			{"model_name": "llama", "model": "openai/llama", "api_base": "https://inference.example/v1", "api_key": "k"},
			{"model_name": "whisper", "model": "nvidia/openai/whisper-large-v3", "api_key": "k"},
			{"model_name": "kept-off", "model": "anthropic/claude-x", "api_key": "k", "capabilities": {"images": false}}
		]
	}`)

	want := map[string]bool{
		"haiku": true, "luna": true, "gpt": true, "glm": true,
		"llama": false, "whisper": false, "kept-off": false,
	}
	got := acceptsImagesByName(cfg)
	for name, accepts := range want {
		if got[name] != accepts {
			t.Errorf("%s accepts images = %v, want %v", name, got[name], accepts)
		}
	}
	for _, m := range cfg.ModelList {
		if m.Capabilities == nil || m.Capabilities.Images == nil {
			t.Errorf("%s has no explicit capabilities.images after migration", m.ModelName)
		}
	}
}

func TestVersion4ConfigTreatsUnsetImageFlagAsTextOnly(t *testing.T) {
	cfg := loadImageCapabilityConfig(t, `{
		"version": 4,
		"agents": {"defaults": {"model_name": "haiku"}},
		"model_list": [{"model_name": "haiku", "model": "anthropic/claude-haiku-5-5", "api_key": "k"}]
	}`)

	if acceptsImagesByName(cfg)["haiku"] {
		t.Fatal("version 4 model without capabilities.images accepts images")
	}
}
