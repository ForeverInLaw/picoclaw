package providers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// keyPoolConfig defines one Key pool with the given keys, all enabled, and two
// configured models (Opus and Haiku) that reference it.
func keyPoolConfig(t *testing.T, apiBase string, keys ...string) *config.Config {
	t.Helper()
	pool := config.KeyPool{ID: "orgs", Name: "Anthropic organizations"}
	for i, key := range keys {
		pool.Keys = append(pool.Keys, config.KeyPoolKey{
			ID:      fmt.Sprintf("org-%d", i+1),
			Label:   fmt.Sprintf("Org %d", i+1),
			Enabled: true,
			APIKey:  key,
		})
	}
	cfg := &config.Config{
		KeyPools: []config.KeyPool{pool},
		ModelList: []config.ModelConfig{
			{ModelName: "opus", Model: "anthropic/claude-opus-5-5", APIBase: apiBase, KeyPoolID: "orgs"},
			{ModelName: "haiku", Model: "anthropic/claude-haiku-4-5", APIBase: apiBase, KeyPoolID: "orgs"},
		},
	}
	if err := cfg.ResolveKeyPools(); err != nil {
		t.Fatalf("ResolveKeyPools() error = %v", err)
	}
	return cfg
}

// okAnswer is a successful Messages API response.
const okAnswer = `{"id":"msg_p","type":"message","role":"assistant","model":"claude-opus-5-5",
	"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

func newAnswering(t *testing.T) *fakeAnthropic {
	return newFakeAnthropic(t, func(w http.ResponseWriter) { writeJSON(w, okAnswer) })
}

// keyFor sends one request of the conversation through provider and returns
// the API key the fake Anthropic received for it.
func (f *fakeAnthropic) keyFor(t *testing.T, provider LLMProvider, modelID, conversation string) string {
	t.Helper()
	_, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, modelID,
		map[string]any{"max_tokens": 64, ConversationIdentityOption: conversation})
	if err != nil {
		t.Fatalf("Chat(%q) error = %v", conversation, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1].header.Get("X-Api-Key")
}

func createProvider(t *testing.T, model *config.ModelConfig) (LLMProvider, string) {
	t.Helper()
	provider, modelID, err := CreateProviderFromConfig(model)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig(%s) error = %v", model.ModelName, err)
	}
	return provider, modelID
}

func TestKeyPoolModelKeepsAConversationOnOneKey(t *testing.T) {
	fake := newAnswering(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	provider, modelID := createProvider(t, &cfg.ModelList[0])

	first := fake.keyFor(t, provider, modelID, "agent:main:telegram:direct:42")
	for range 3 {
		if got := fake.keyFor(t, provider, modelID, "agent:main:telegram:direct:42"); got != first {
			t.Fatalf("later request used key %q, want %q", got, first)
		}
	}
	restarted, modelID := createProvider(t, &keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b").ModelList[0])
	if got := fake.keyFor(t, restarted, modelID, "agent:main:telegram:direct:42"); got != first {
		t.Fatalf("new provider instance used key %q, want %q", got, first)
	}
}

func TestKeyPoolModelSpreadsConversationsAcrossEnabledKeys(t *testing.T) {
	fake := newAnswering(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c")
	provider, modelID := createProvider(t, &cfg.ModelList[0])

	served := map[string]int{}
	for i := range 60 {
		served[fake.keyFor(t, provider, modelID, fmt.Sprintf("agent:main:telegram:direct:%d", i))]++
	}
	for _, key := range []string{"sk-ant-a", "sk-ant-b", "sk-ant-c"} {
		if served[key] < 5 {
			t.Errorf("key %s served %d of 60 conversations, want a share; spread = %v", key, served[key], served)
		}
	}
}

func TestDisablingAPoolKeyMovesOnlyItsConversations(t *testing.T) {
	fake := newAnswering(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c")
	provider, modelID := createProvider(t, &cfg.ModelList[0])
	before := map[string]string{}
	for i := range 60 {
		conversation := fmt.Sprintf("agent:main:telegram:group:%d", i)
		before[conversation] = fake.keyFor(t, provider, modelID, conversation)
	}

	cfg.KeyPools[0].Keys[1].Enabled = false // sk-ant-b
	if err := cfg.ResolveKeyPools(); err != nil {
		t.Fatalf("ResolveKeyPools() error = %v", err)
	}
	provider, modelID = createProvider(t, &cfg.ModelList[0])

	moved := 0
	for conversation, previous := range before {
		got := fake.keyFor(t, provider, modelID, conversation)
		switch {
		case got == "sk-ant-b":
			t.Errorf("%s still uses the disabled key", conversation)
		case previous == "sk-ant-b":
			moved++
		case got != previous:
			t.Errorf("%s moved from %s to %s although its key stayed enabled", conversation, previous, got)
		}
	}
	if moved == 0 {
		t.Fatal("no conversation was bound to the disabled key; the test needs one")
	}
}

func TestModelsSharingAKeyPoolUseTheSameKeyForAConversation(t *testing.T) {
	fake := newAnswering(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c")
	opus, opusID := createProvider(t, &cfg.ModelList[0])
	haiku, haikuID := createProvider(t, &cfg.ModelList[1])

	for i := range 20 {
		conversation := fmt.Sprintf("agent:main:pico:direct:%d", i)
		if a, b := fake.keyFor(t, opus, opusID, conversation), fake.keyFor(t, haiku, haikuID, conversation); a != b {
			t.Errorf("%s used %s with opus and %s with haiku", conversation, a, b)
		}
	}
}

// loadConfigFile writes a config.json and loads it the way the gateway does.
func loadConfigFile(t *testing.T, document string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg
}

func modelNamed(t *testing.T, cfg *config.Config, name string) *config.ModelConfig {
	t.Helper()
	model, err := cfg.GetModelConfig(name)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestAnthropicModelWithAPIKeysLoadsAsAKeyPoolModel(t *testing.T) {
	fake := newAnswering(t)
	cfg := loadConfigFile(t, `{"version":3,"model_list":[
		{"model_name":"claude","model":"anthropic/claude-opus-5-5","api_base":"`+fake.URL+`",
		 "api_keys":["sk-ant-a","sk-ant-b"]}]}`)
	provider, modelID := createProvider(t, modelNamed(t, cfg, "claude"))

	served := map[string]bool{}
	for i := range 30 {
		served[fake.keyFor(t, provider, modelID, fmt.Sprintf("agent:main:telegram:direct:%d", i))] = true
	}
	if !served["sk-ant-a"] || !served["sk-ant-b"] {
		t.Fatalf("conversations were served by %v, want both migrated keys", served)
	}
	if _, err := cfg.GetModelConfig("claude__key_1"); err == nil {
		t.Error("migrated keys still expanded into ordered failover entries")
	}
	restarted := loadConfigFile(t, `{"version":3,"model_list":[
		{"model_name":"claude","model":"anthropic/claude-opus-5-5","api_base":"`+fake.URL+`",
		 "api_keys":["sk-ant-a","sk-ant-b"]}]}`)
	again, modelID := createProvider(t, modelNamed(t, restarted, "claude"))
	first := fake.keyFor(t, provider, modelID, "agent:main:telegram:direct:7")
	if got := fake.keyFor(t, again, modelID, "agent:main:telegram:direct:7"); got != first {
		t.Fatalf("after reloading the unsaved migration the conversation moved from %s to %s", first, got)
	}
}

func TestKeyPoolWithoutEnabledKeysLeavesTheCandidateUnavailable(t *testing.T) {
	cfg := keyPoolConfig(t, "http://127.0.0.1:1", "sk-ant-a")
	cfg.KeyPools[0].Keys[0].Enabled = false

	_, _, err := CreateProviderFromConfig(&cfg.ModelList[0])

	var unavailable *CandidateUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("CreateProviderFromConfig() error = %v, want an unavailable candidate for the Routing chain", err)
	}
}

func TestKeyPoolModelCachesAndLogsUsageByKeyLabel(t *testing.T) {
	fake := newAnswering(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-secret-a", "sk-ant-secret-b")
	cfg.ModelList[0].CacheTTL = "1h"
	provider, modelID := createProvider(t, &cfg.ModelList[0])

	var key string
	logged := usageLog(t, func() { key = fake.keyFor(t, provider, modelID, "agent:main:telegram:direct:42") })

	assertJSON(t, "cache_control", fake.only(t).body["cache_control"], `{"type":"ephemeral","ttl":"1h"}`)
	wantLabel := map[string]string{"sk-ant-secret-a": "Org 1", "sk-ant-secret-b": "Org 2"}[key]
	if len(logged) != 1 || logged[0]["key"] != wantLabel {
		t.Fatalf("usage lines = %v, want one labeled %q", logged, wantLabel)
	}
	for _, line := range logged {
		for _, value := range line {
			if s, _ := value.(string); strings.Contains(s, "sk-ant-secret") {
				t.Fatalf("usage line reveals a key secret: %v", line)
			}
		}
	}
}

func TestMigratedSidecarKeysStayInTheSidecar(t *testing.T) {
	fake := newAnswering(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	sidecar := filepath.Join(dir, config.SecurityConfigFile)
	document := `{"version":3,"model_list":[
		{"model_name":"claude","model":"anthropic/claude-opus-5-5","api_base":"` + fake.URL + `"}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar,
		[]byte("model_list:\n  claude:\n    api_keys: [sk-ant-sidecar-a, sk-ant-sidecar-b]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "sk-ant-sidecar") {
		t.Fatalf("config.json received a sidecar secret:\n%s", saved)
	}
	if !strings.Contains(string(saved), `"key_pool_id"`) {
		t.Fatalf("config.json does not reference the migrated pool:\n%s", saved)
	}
	secrets, _ := os.ReadFile(sidecar)
	if !strings.Contains(string(secrets), "sk-ant-sidecar-a") ||
		!strings.Contains(string(secrets), "sk-ant-sidecar-b") {
		t.Fatalf("sidecar lost the pool keys:\n%s", secrets)
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("reloading the migrated config: %v", err)
	}
	provider, modelID := createProvider(t, modelNamed(t, reloaded, "claude"))
	served := map[string]bool{}
	for i := range 30 {
		served[fake.keyFor(t, provider, modelID, fmt.Sprintf("agent:main:telegram:direct:%d", i))] = true
	}
	if !served["sk-ant-sidecar-a"] || !served["sk-ant-sidecar-b"] || len(served) != 2 {
		t.Fatalf("reloaded model was served by %v, want both sidecar keys", served)
	}
}
