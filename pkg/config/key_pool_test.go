package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const migratableAnthropicConfig = `{"version":3,"model_list":[
	{"model_name":"opus","model":"anthropic/claude-opus-5-5","api_keys":["sk-ant-first-org","sk-ant-second-org"]},
	{"model_name":"haiku","model":"anthropic/claude-haiku-4-5","api_keys":["sk-ant-first-org","sk-ant-second-org"]},
	{"model_name":"glm","model":"zhipu/glm-4.7","api_keys":["zhipu-1","zhipu-2"]}]}`

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// savedDocument reads back what SaveConfig wrote.
func savedDocument(t *testing.T, path string) (raw string, doc struct {
	ModelList []ModelConfig `json:"model_list"`
	KeyPools  []KeyPool     `json:"key_pools"`
},
) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return string(data), doc
}

func TestAnthropicAPIKeysMigrationIsSavedOnTheNextWrite(t *testing.T) {
	path := writeConfig(t, migratableAnthropicConfig)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err = SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	_, doc := savedDocument(t, path)
	if len(doc.KeyPools) != 1 {
		t.Fatalf("saved %d key pools, want one shared by opus and haiku: %+v", len(doc.KeyPools), doc.KeyPools)
	}
	pool := doc.KeyPools[0]
	if len(pool.Keys) != 2 || pool.Keys[0].APIKey != "sk-ant-first-org" || pool.Keys[1].APIKey != "sk-ant-second-org" ||
		!pool.Keys[0].Enabled || !pool.Keys[1].Enabled {
		t.Fatalf("saved pool keys = %+v, want both keys enabled in order", pool.Keys)
	}
	for _, m := range doc.ModelList {
		switch m.ModelName {
		case "opus", "haiku":
			if m.KeyPoolID != pool.ID || m.APIKey != "" || len(m.APIKeys) != 0 {
				t.Errorf("saved %s = pool %q key %q keys %v, want only the pool reference",
					m.ModelName, m.KeyPoolID, m.APIKey, m.APIKeys)
			}
		case "glm":
			if m.KeyPoolID != "" || len(m.APIKeys) != 2 {
				t.Errorf("saved glm = pool %q keys %v, want its api_keys unchanged", m.KeyPoolID, m.APIKeys)
			}
		}
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reloading the migrated config: %v", err)
	}
	opus, _ := reloaded.GetModelConfig("opus")
	if opus.KeyPool() == nil || opus.KeyPool().ID != pool.ID {
		t.Fatalf("reloaded opus is not bound to the saved pool")
	}
	if _, err := reloaded.GetModelConfig("glm__key_1"); err != nil {
		t.Errorf("other providers lost their api_keys failover: %v", err)
	}
}

func TestKeyPoolSecretsAreSealedWithThePassphrase(t *testing.T) {
	t.Setenv("PICOCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)
	path := writeConfig(t, migratableAnthropicConfig)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err = SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	raw, doc := savedDocument(t, path)
	if strings.Contains(raw, "sk-ant-first-org") || strings.Contains(raw, "sk-ant-second-org") {
		t.Fatal("saved config contains a plaintext Key pool secret")
	}
	if !strings.HasPrefix(doc.KeyPools[0].Keys[0].APIKey, "enc://") {
		t.Fatalf("saved key = %q, want an enc:// credential", doc.KeyPools[0].Keys[0].APIKey)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.KeyPools[0].Keys[0].APIKey; got != "sk-ant-first-org" {
		t.Fatalf("reloaded key = %q, want the decrypted secret", got)
	}
}

func TestPlaintextSecretWarningsCoverKeyPoolSecrets(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{{ModelName: "own", APIKey: "sk-plain"}, {ModelName: "sealed", APIKey: "enc://x"}},
		KeyPools: []KeyPool{{ID: "orgs", Name: "Orgs", Keys: []KeyPoolKey{
			{ID: "a", Label: "First", APIKey: "sk-ant-plain"},
			{ID: "b", Label: "Second", APIKey: "file://second.key"},
		}}},
	}
	got := cfg.plaintextSecretWarnings()
	if len(got) != 2 || !strings.Contains(got[0], `model "own"`) ||
		!strings.Contains(got[1], `key "First" of Key pool "Orgs"`) {
		t.Fatalf("warnings = %q, want the plaintext model key and pool key", got)
	}
}

func TestAModelHoldsItsKeyPoolAsResolved(t *testing.T) {
	cfg := &Config{
		KeyPools:  []KeyPool{{ID: "orgs", Name: "Orgs", Keys: []KeyPoolKey{{ID: "a", Enabled: true, APIKey: "sk-a"}}}},
		ModelList: []ModelConfig{{ModelName: "opus", Model: "anthropic/claude-opus-5-5", KeyPoolID: "orgs"}},
	}
	if err := cfg.ResolveKeyPools(); err != nil {
		t.Fatal(err)
	}
	cfg.KeyPools[0].Keys[0].APIKey = "sk-edited"
	cfg.KeyPools = append(cfg.KeyPools, KeyPool{ID: "spare"})
	if got := cfg.ModelList[0].KeyPool().Keys[0].APIKey; got != "sk-a" {
		t.Fatalf("pool after unresolved edits = %q, want the resolved key", got)
	}
	if err := cfg.ResolveKeyPools(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelList[0].KeyPool().Keys[0].APIKey; got != "sk-edited" {
		t.Fatalf("pool after resolving again = %q, want the edited key", got)
	}
}
