package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/credential"
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

func TestSeparatelyEncryptedEqualKeyListsMigrateIntoOneSealedPool(t *testing.T) {
	t.Setenv("PICOCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)
	secrets := []string{"sk-ant-first-org", "sk-ant-second-org"}
	// Each model's key list is encrypted on its own, so equal keys differ on disk.
	sealedList := func() []string {
		sealed := make([]string, len(secrets))
		for i, secret := range secrets {
			value, err := credential.Encrypt("test-passphrase", "", secret)
			if err != nil {
				t.Fatal(err)
			}
			sealed[i] = value
		}
		return sealed
	}
	opusKeys, haikuKeys := sealedList(), sealedList()
	if opusKeys[0] == haikuKeys[0] {
		t.Fatal("setup: the two encryptions of one key are identical")
	}
	models := []ModelConfig{
		{ModelName: "opus", Model: "anthropic/claude-opus-5-5", APIKeys: opusKeys},
		{ModelName: "haiku", Model: "anthropic/claude-haiku-4-5", APIKeys: haikuKeys},
	}

	// Migration matches the lists by value but keeps the references themselves.
	migrated := &Config{ModelList: slices.Clone(models)}
	migrated.migrateAnthropicAPIKeysToPools(t.TempDir())
	if len(migrated.KeyPools) != 1 {
		t.Fatalf("migrated into %d key pools, want one shared by opus and haiku", len(migrated.KeyPools))
	}
	for i, key := range migrated.KeyPools[0].Keys {
		if key.APIKey != opusKeys[i] {
			t.Errorf("pool key %d = %q, want the first model's enc:// reference", i, key.APIKey)
		}
	}

	// Loading and saving that configuration writes one pool and no plaintext.
	document, err := json.Marshal(map[string]any{"version": 3, "model_list": models})
	if err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, string(document))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err = SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	raw, doc := savedDocument(t, path)
	if len(doc.KeyPools) != 1 {
		t.Fatalf("saved %d key pools, want one", len(doc.KeyPools))
	}
	for i, secret := range secrets {
		if strings.Contains(raw, secret) {
			t.Fatalf("saved config contains the plaintext secret %q", secret)
		}
		if key := doc.KeyPools[0].Keys[i].APIKey; !strings.HasPrefix(key, "enc://") {
			t.Errorf("saved pool key %d = %q, want an enc:// credential", i, key)
		}
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, secret := range secrets {
		if got := reloaded.KeyPools[0].Keys[i].APIKey; got != secret {
			t.Errorf("reloaded pool key %d = %q, want %q", i, got, secret)
		}
	}
}
