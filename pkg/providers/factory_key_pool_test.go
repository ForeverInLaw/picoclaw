package providers

import (
	"fmt"
	"net/http"
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

func newAnswering(t *testing.T) *fakeAnthropic {
	return newFakeAnthropic(t, func(w http.ResponseWriter) {
		writeJSON(w, `{"id":"msg_p","type":"message","role":"assistant","model":"claude-opus-5-5",
			"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	})
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
