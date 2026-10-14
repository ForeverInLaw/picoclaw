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
