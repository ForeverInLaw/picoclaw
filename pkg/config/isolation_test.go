package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsolationConfigDefaultsRoundTripAndEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	cfg, err := LoadConfig(filepath.Join(dir, "missing.json"))
	require.NoError(t, err)
	require.False(t, cfg.Isolation.Enabled)
	require.False(t, cfg.Isolation.DisableNetwork)
	cfg.Isolation = IsolationConfig{Enabled: true, EnvAllowlist: []string{"DECLARED_VALUE"}, ExposePaths: []ExposePath{
		{Source: dir, Target: "/mnt/data", Mode: "ro"},
	}}
	path := filepath.Join(dir, "saved.json")
	require.NoError(t, SaveConfig(path, cfg))
	reopened, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, cfg.Isolation, reopened.Isolation)
	t.Setenv("PICOCLAW_ISOLATION_ENABLED", "false")
	t.Setenv("PICOCLAW_ISOLATION_DISABLE_NETWORK", "true")
	t.Setenv("PICOCLAW_ISOLATION_ENV_ALLOWLIST", "FIRST,SECOND")
	overridden, err := LoadConfig(path)
	require.NoError(t, err)
	require.False(t, overridden.Isolation.Enabled)
	require.True(t, overridden.Isolation.DisableNetwork)
	require.Equal(t, []string{"FIRST", "SECOND"}, overridden.Isolation.EnvAllowlist)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved Config
	require.NoError(t, json.Unmarshal(contents, &saved))
	require.True(t, saved.Isolation.Enabled, "environment overrides changed the saved source")
}
