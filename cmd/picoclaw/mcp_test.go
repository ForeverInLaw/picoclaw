package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func executeMCP(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewPicoclawCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(bytes.NewBufferString(""))
	cmd.SetArgs(append([]string{"mcp"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return output.String(), err
}

func TestMCPCLIAddsStdioServerThroughExistingV3Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv(config.EnvConfig, path)
	input := `{"version":3,"agents":{"defaults":{"workspace":"WORKSPACE","context_manager":"legacy","memory_index":{"enabled":true}}},"model_list":[{"model_name":"custom","model":"openai/custom","api_keys":["first-dummy-key","second-dummy-key"]}],"channel_list":{"work":{"enabled":false,"type":"telegram","settings":{"token":"channel-dummy-secret","inline":{"enabled":true}}}},"tools":{"mcp":{"enabled":false,"discovery":{"enabled":true,"use_bm25":true}}}}`
	input = string(bytes.ReplaceAll([]byte(input), []byte("WORKSPACE"), []byte(filepath.ToSlash(dir))))
	require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
	securityPath := filepath.Join(dir, config.SecurityConfigFile)
	require.NoError(t, os.WriteFile(securityPath, []byte("model_list:\n  custom:\n    api_keys: [first-dummy-key, second-dummy-key]\n"), 0o600))
	output, err := executeMCP(t, "add", "--env", "ACCESS_TOKEN=stdio-dummy-secret", "--env-file", ".env.mcp", "--deferred", "local", "--", "npx", "-y", "fake-mcp", "--port", "4000")
	require.NoError(t, err, output)
	require.Contains(t, output, `MCP server "local" saved`)
	require.NotContains(t, output, "stdio-dummy-secret")
	reopened, err := config.LoadConfig(path)
	require.NoError(t, err)
	require.True(t, reopened.Tools.MCP.Enabled)
	server := reopened.Tools.MCP.Servers["local"]
	require.Equal(t, "npx", server.Command)
	require.Equal(t, []string{"-y", "fake-mcp", "--port", "4000"}, server.Args)
	require.Equal(t, "stdio-dummy-secret", server.Env["ACCESS_TOKEN"])
	require.Equal(t, ".env.mcp", server.EnvFile)
	require.NotNil(t, server.Deferred)
	require.True(t, *server.Deferred)
	require.True(t, reopened.Tools.MCP.Discovery.Enabled)
	require.True(t, reopened.Agents.Defaults.MemoryIndex.Enabled)
	require.Equal(t, "legacy", reopened.Agents.Defaults.ContextManager)
	channel, err := reopened.ChannelConfig("work")
	require.NoError(t, err)
	require.Equal(t, "channel-dummy-secret", channel.Channels.Telegram.Token)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"api_keys": [`)
	require.Contains(t, string(data), "second-dummy-key")
	security, err := os.ReadFile(securityPath)
	require.NoError(t, err)
	require.Contains(t, string(security), "second-dummy-key")
}

func TestMCPCLIUpdatePreservesUnspecifiedServerOptionsAndRemoveReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	cfg := config.DefaultConfig()
	deferred := true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"local":  {Enabled: false, Command: "original", Args: []string{"old-arg"}, Env: map[string]string{"KEEP": "secret-kept", "REPLACE": "old"}, EnvFile: ".env.mcp", Deferred: &deferred},
		"remote": {Enabled: false, Type: "streamable-http", URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "Bearer header-secret", "X-Mode": "kept"}},
	}
	require.NoError(t, config.SaveConfig(path, cfg))
	output, err := executeMCP(t, "add", "--force", "--env", "REPLACE=new", "local", "python", "-u", "server.py")
	require.NoError(t, err, output)
	reopened, err := config.LoadConfig(path)
	require.NoError(t, err)
	local := reopened.Tools.MCP.Servers["local"]
	require.False(t, local.Enabled, "updating did not explicitly enable a disabled server")
	require.Equal(t, "secret-kept", local.Env["KEEP"])
	require.Equal(t, "new", local.Env["REPLACE"])
	require.Equal(t, ".env.mcp", local.EnvFile)
	require.True(t, *local.Deferred)
	require.Equal(t, []string{"-u", "server.py"}, local.Args)
	output, err = executeMCP(t, "update", "remote", "https://replacement.test/mcp", "--header", "X-Mode=updated")
	require.NoError(t, err, output)
	reopened, err = config.LoadConfig(path)
	require.NoError(t, err)
	remote := reopened.Tools.MCP.Servers["remote"]
	require.Equal(t, "Bearer header-secret", remote.Headers["Authorization"])
	require.Equal(t, "updated", remote.Headers["X-Mode"])
	require.False(t, remote.Enabled)
	output, err = executeMCP(t, "remove", "local")
	require.NoError(t, err, output)
	reopened, err = config.LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, reopened.Tools.MCP.Servers, 1)
	require.Equal(t, remote, reopened.Tools.MCP.Servers["remote"])
	_, err = executeMCP(t, "update", "missing", "npx")
	require.ErrorContains(t, err, "not found")
}
