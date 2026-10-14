package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
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

func TestMCPCLIStdioProcess(t *testing.T) {
	if os.Getenv("CLI_FAKE_STDIO") != "1" {
		return
	}
	observed, _ := json.Marshal([]string{os.Getenv("FROM_FILE"), os.Getenv("OVERRIDE")})
	if err := os.WriteFile(os.Getenv("OBSERVATION"), observed, 0o600); err != nil {
		os.Exit(2)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "local", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "environment_probe", Description: fmt.Sprintf("%s %s", os.Getenv("FROM_FILE"), os.Getenv("OVERRIDE")), InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		_ = os.WriteFile(os.Getenv("TOOL_MARKER"), []byte("called"), 0o600)
		return &sdkmcp.CallToolResult{}, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestMCPCLIStdioProbeUsesEnvFileOverridesAndConfiguredIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv(config.EnvConfig, path)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.mcp"), []byte("CLI_FAKE_STDIO=1\nFROM_FILE=file-only-secret\nOVERRIDE=from-file\n"), 0o600))
	observed := filepath.Join(dir, "observed.json")
	marker := filepath.Join(dir, "tool-called")
	cfg := config.DefaultConfig()
	cfg.Tools.FilterSensitiveData = false
	cfg.Agents.Defaults.Workspace = dir
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{"local": {
		Enabled: false, Command: os.Args[0], Args: []string{"-test.run=^TestMCPCLIStdioProcess$"}, EnvFile: ".env.mcp",
		Env: map[string]string{"OVERRIDE": "from-config", "OBSERVATION": observed, "TOOL_MARKER": marker},
	}}
	require.NoError(t, config.SaveConfig(path, cfg))
	output, err := executeMCP(t, "test", "local")
	require.NoError(t, err, output)
	data, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.JSONEq(t, "[\"file-only-secret\",\"from-config\"]", string(data))
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
	server := cfg.Tools.MCP.Servers["local"]
	server.Enabled = true
	cfg.Tools.MCP.Servers["local"] = server
	require.NoError(t, config.SaveConfig(path, cfg))
	output, err = executeMCP(t, "show", "local")
	require.NoError(t, err, output)
	require.NotContains(t, output, "file-only-secret")
	require.NotContains(t, output, "from-config")
	if runtime.GOOS != "linux" {
		return
	}
	cfg.Isolation.Enabled = true
	sandboxMarker := filepath.Join(dir, "unisolated-start")
	cfg.Tools.MCP.Servers["local"] = config.MCPServerConfig{Enabled: true, Command: "/bin/sh", Args: []string{"-c", "touch '" + sandboxMarker + "'"}}
	require.NoError(t, config.SaveConfig(path, cfg))
	t.Setenv("PATH", t.TempDir())
	output, err = executeMCP(t, "test", "local")
	require.ErrorContains(t, err, "isolation")
	_, err = os.Stat(sandboxMarker)
	require.ErrorIs(t, err, os.ErrNotExist, output)
}

func TestMCPCLIReadOnlyProbesAndRedactedDiagnostics(t *testing.T) {
	var initializes, lists, calls atomic.Int32
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "server http-secret", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name: "read_echo", Description: "Read evidence http-secret query-secret",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)
		return &sdkmcp.CallToolResult{}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			require.Equal(t, "Bearer http-secret", r.Header.Get("Authorization"))
			require.Equal(t, "declared-value", r.Header.Get("X-Declared"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var request struct {
				Method string `json:"method"`
			}
			require.NoError(t, json.Unmarshal(body, &request))
			if request.Method == "initialize" {
				initializes.Add(1)
			}
			if request.Method == "tools/list" {
				lists.Add(1)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv(config.EnvConfig, path)
	cfg := config.DefaultConfig()
	cfg.Tools.FilterSensitiveData = false
	cfg.Isolation.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"remote":   {Enabled: true, Type: "streamable-http", URL: strings.Replace(endpoint.URL, "http://", "http://user-secret:password-secret@", 1) + "?token=query-secret", Headers: map[string]string{"Authorization": "Bearer http-secret", "X-Declared": "declared-value"}},
		"disabled": {Enabled: false, Command: "npx", Args: []string{"argument-secret"}, Env: map[string]string{"TOKEN": "env-secret"}},
	}
	require.NoError(t, config.SaveConfig(path, cfg))
	logPath := filepath.Join(dir, "diagnostics.log")
	level := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	require.NoError(t, logger.EnableFileLogging(logPath))
	defer func() { logger.DisableFileLogging(); logger.SetLevel(level) }()
	for _, args := range [][]string{{"list"}, {"show", "disabled"}, {"list", "--status"}, {"show", "remote"}, {"test", "remote"}} {
		output, err := executeMCP(t, args...)
		require.NoError(t, err, output)
		for _, secret := range []string{"http-secret", "user-secret", "password-secret", "query-secret", "argument-secret", "env-secret"} {
			require.NotContains(t, output, secret)
		}
		if args[0] == "show" && args[1] == "remote" {
			require.Contains(t, output, "read_echo")
			require.Contains(t, output, "path")
		}
	}
	require.Equal(t, int32(3), initializes.Load())
	require.Equal(t, int32(3), lists.Load())
	require.Zero(t, calls.Load(), "probes must never invoke tools")
	logs, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.NotContains(t, string(logs), "http-secret")
	require.NotContains(t, string(logs), "query-secret")
	_, err = executeMCP(t, "test", "missing")
	require.ErrorContains(t, err, "not found")
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
