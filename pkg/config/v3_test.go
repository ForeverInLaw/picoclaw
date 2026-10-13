package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyForkConfigMigratesWithoutChangingSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":2,"agents":{"defaults":{"model":"custom","memory_index":{"enabled":true,"embeddings":{"enabled":true,"model_name":"embed","dimensions":512}}}},"model_list":[{"model_name":"custom","model":"openai/custom","api_keys":["first-secret","second-secret"]}],"channels":{"telegram":{"enabled":true,"token":"telegram-secret","inline":{"enabled":true},"participant_aliases":{"42":"Alice"}},"email":{"enabled":true,"imap_host":"imap.example","smtp_host":"smtp.example","password":"email-secret"}},"tools":{"web":{"exa":{"enabled":true,"api_keys":["exa-secret"]},"parallel":{"enabled":true,"api_keys":["parallel-secret"]}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o400); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if decodeErr := json.Unmarshal(data, &saved); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if string(saved["version"]) != "3" || saved["channel_list"] == nil || saved["channels"] != nil {
		t.Fatalf("expected v3 channel_list, got %s", data)
	}
	if cfg.Channels.Telegram.Token != "telegram-secret" || !cfg.Channels.Telegram.Inline.Enabled ||
		cfg.Channels.Email.Password != "email-secret" ||
		cfg.Agents.Defaults.MemoryIndex.Embeddings.Dimensions != 512 {
		t.Fatal("fork settings lost")
	}
	source, _ := os.ReadFile(path)
	if string(source) != legacy {
		t.Fatal("load changed read-only source")
	}
	out := filepath.Join(dir, "prepared.json")
	if saveErr := SaveConfig(out, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	reopened, err := LoadConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Channels.Telegram.Token != "telegram-secret" || reopened.Channels.Email.Password != "email-secret" ||
		len(reopened.ModelList) != 2 ||
		reopened.Tools.Web.Exa.APIKeys[0] != "exa-secret" ||
		reopened.Tools.Web.Parallel.APIKeys[0] != "parallel-secret" {
		t.Fatal("round trip lost credentials")
	}
}

func TestConfigUnknownFieldsIncludeRepairPath(t *testing.T) {
	for _, input := range []string{
		`{"channels":{"telegram":{"tokne":"x"}}}`,
		`{"version":3,"channel_list":{"work":{"type":"telegram","settings":{"tokne":"x"}}}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "tokne") ||
			!strings.Contains(err.Error(), "telegram") && !strings.Contains(err.Error(), "work") {
			t.Fatalf("expected actionable unknown field, got %v", err)
		}
	}
}

func TestV3SecurityAndEnvironmentStayWithTheirInstance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	input := `{"version":3,"agents":{"defaults":{"model_name":"custom"}},"model_list":[{"model_name":"custom","model":"openai/custom"}],"channel_list":{"telegram":{"enabled":true,"type":"telegram","settings":{"token":"original"}},"work":{"enabled":true,"type":"telegram","settings":{"token":"work-secret"}},"email":{"enabled":true,"type":"email","settings":{"password":"email-original"}}}}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	security := "model_list:\n  custom:\n    api_keys: [model-secret]\nchannels:\n  telegram:\n    token: security-secret\nchannel_list:\n  work:\n    settings:\n      token: work-security-secret\n  email:\n    settings:\n      password: email-security-secret\n"
	if err := os.WriteFile(filepath.Join(dir, SecurityConfigFile), []byte(security), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_CHANNELS_TELEGRAM_TOKEN", "legacy-env-secret")
	t.Setenv("PICOCLAW_CHANNEL_LIST_WORK_TOKEN", "named-env-secret")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	work, err := cfg.ChannelConfig("work")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channels.Telegram.Token != "legacy-env-secret" || work.Channels.Telegram.Token != "named-env-secret" ||
		cfg.Channels.Email.Password != "email-security-secret" ||
		cfg.ModelList[0].APIKey != "model-secret" {
		t.Fatal("security or environment overrides lost or crossed instance boundary")
	}
	for _, secret := range []string{"legacy-env-secret", "named-env-secret", "email-security-secret", "model-secret"} {
		if strings.Contains(cfg.FilterSensitiveData("tool output: "+secret), secret) {
			t.Fatalf("secret %s was not sanitized", secret)
		}
	}
}

func TestSaveReloadRetainsEditedSidecarCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		path,
		[]byte(
			`{"model_list":[{"model_name":"custom","model":"openai/custom"}],"channels":{"telegram":{"token":"inline-secret"}}}`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, SecurityConfigFile),
		[]byte(
			"model_list:\n  custom:\n    api_keys: [old-model-secret]\nchannels:\n  telegram:\n    token: old-telegram-secret\n",
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels.Telegram.Token = "changed-telegram-secret"
	cfg.ModelList[0].APIKey = "changed-model-secret"
	if saveErr := SaveConfig(path, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Channels.Telegram.Token != "changed-telegram-secret" ||
		reloaded.ModelList[0].APIKey != "changed-model-secret" {
		t.Fatal("sidecar restored stale credentials after save")
	}
}

func TestPreparedMultiKeyConfigReopensBesideUnchangedSecurity(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "config.json"), filepath.Join(dir, "prepared.json")
	if err := os.WriteFile(
		source,
		[]byte(`{"model_list":[{"model_name":"custom","model":"openai/custom"}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, SecurityConfigFile),
		[]byte("model_list:\n  custom:\n    api_keys: [first-secret,second-secret]\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := PrepareConfig(source, output); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(output)
	if err != nil || len(reloaded.ModelList) != 2 {
		t.Fatalf("preparation duplicated key-level configurations: %d %v", len(reloaded.ModelList), err)
	}
}

func TestUnversionedProviderConfigUsesItsConfiguredModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"agents":{"defaults":{"provider":"openai","model":"custom-model"}},"providers":{"openai":{"api_key":"provider-secret","api_base":"https://example.invalid/v1"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	model, err := cfg.GetModelConfig(cfg.Agents.Defaults.GetModelName())
	if err != nil || model.Model != "openai/custom-model" || model.APIKey != "provider-secret" ||
		model.APIBase != "https://example.invalid/v1" {
		t.Fatalf("legacy provider did not retain configured model: %+v %v", model, err)
	}
}

func TestSaveSealsMultiKeyArrayWithoutChangingRuntimeKeys(t *testing.T) {
	t.Setenv("PICOCLAW_KEY_PASSPHRASE", "test-passphrase")
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.ModelList = []ModelConfig{
		{ModelName: "custom", Model: "openai/custom", APIKeys: []string{"array-one-secret", "array-two-secret"}},
	}
	if saveErr := SaveConfig(path, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "array-one-secret") || strings.Contains(string(raw), "array-two-secret") ||
		cfg.ModelList[0].APIKeys[0] != "array-one-secret" {
		t.Fatal("multi-key credentials were exposed or runtime keys mutated")
	}
	reloaded, err := LoadConfig(path)
	if err != nil || len(reloaded.ModelList) != 2 {
		t.Fatalf("sealed multi-key array failed to reload: %v", err)
	}
}
