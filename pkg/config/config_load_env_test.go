package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_AppliesEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("PICOCLAW_TOOLS_WEB_PRIMARY_PROVIDER", "exa")
	t.Setenv("PICOCLAW_TOOLS_WEB_PARALLEL_ENABLED", "true")
	t.Setenv("PICOCLAW_TOOLS_WEB_PARALLEL_API_KEYS", "parallel-one,parallel-two")
	t.Setenv("PICOCLAW_TOOLS_WEB_EXA_ENABLED", "true")
	t.Setenv("PICOCLAW_TOOLS_WEB_EXA_API_KEY", "exa-key")
	t.Setenv("PICOCLAW_CHANNELS_TELEGRAM_ENABLED", "true")
	t.Setenv("PICOCLAW_CHANNELS_TELEGRAM_TOKEN", "telegram-token")
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS", "1234")
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "present"}[exists], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if exists {
				if err := os.WriteFile(
					path,
					[]byte(`{"tools":{"web":{"primary_provider":"parallel"}}}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			parallel := cfg.Tools.Web.Parallel
			if cfg.Tools.Web.PrimaryProvider != "exa" || !parallel.Enabled || len(parallel.APIKeys) != 2 {
				t.Fatal("missing-file and JSON configurations must share search environment processing")
			}
			if !cfg.Tools.Web.Exa.Enabled || cfg.Tools.Web.Exa.APIKey != "exa-key" {
				t.Fatal("Exa environment settings were ignored")
			}
			telegram := cfg.Channels.Telegram
			if !telegram.Enabled || telegram.Token != "telegram-token" || cfg.Agents.Defaults.MaxTokens != 1234 {
				t.Fatal("missing-file and JSON configurations must share channel/model environment processing")
			}

			if err := cfg.ValidateModelList(); err != nil {
				t.Fatalf("default model validation: %v", err)
			}
			if !exists {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("LoadConfig wrote the missing file: %v", err)
				}
			}
		})
	}
}

func TestLoadConfig_MissingFileStillReportsEnvironmentAndSecurityErrors(t *testing.T) {
	t.Run("invalid environment", func(t *testing.T) {
		t.Setenv("PICOCLAW_TOOLS_WEB_EXA_ENABLED", "invalid-boolean")
		if _, err := LoadConfig(filepath.Join(t.TempDir(), "config.json")); err == nil {
			t.Fatal("invalid environment was ignored")
		}
	})
	t.Run("invalid security file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(securityPath(path), []byte("channels: [invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("invalid security file was ignored")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("malformed JSON must still fail")
		}
	})
	t.Run("unreadable path", func(t *testing.T) {
		if _, err := LoadConfig(t.TempDir()); err == nil {
			t.Fatal("a directory must remain a read error")
		}
	})
}
