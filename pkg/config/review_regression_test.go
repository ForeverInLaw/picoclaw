package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigDiagnosticsAcceptsCaseInsensitiveFieldNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"VERSION":3,"AGENTS":{"DEFAULTS":{"MODEL_NAME":"case-variant"}}}`), 0o600))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "case-variant", cfg.Agents.Defaults.ModelName)
}

func TestSaveSecuritySidecarWithOmittedChannelSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(filepath.Join(dir, SecurityConfigFile), []byte("model_list: {}\n"), 0o600))
	cfg := DefaultConfig()
	cfg.ChannelList = ChannelList{"work": {Type: "telegram"}}
	require.NoError(t, SaveConfig(path, cfg))
	reopened, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "telegram", reopened.ChannelList["work"].Type)
}
