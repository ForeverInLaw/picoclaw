//go:build linux

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

// This child runs the real SDK over stdio, with no network or external server.
func TestIsolationMCPServerProcess(t *testing.T) {
	if os.Getenv("IVO_MCP_CHILD") != "1" {
		return
	}
	workspace := os.Getenv("MCP_WORKSPACE")
	if err := os.Chdir(workspace); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	started, err := os.OpenFile("server-started", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = started.WriteString("started\n")
	_ = started.Close()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "isolation-fixture", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			outside, _ := os.ReadFile(os.Getenv("MCP_OUTSIDE_FILE"))
			_, hostProcErr := os.Stat("/proc/" + os.Getenv("MCP_HOST_PID") + "/environ")
			writeErr := os.WriteFile("workspace-output", []byte("written"), 0o600)
			networkAvailable := false
			if endpoint := os.Getenv("MCP_NETWORK_ENDPOINT"); endpoint != "" {
				client := &http.Client{Timeout: 200 * time.Millisecond}
				if response, err := client.Get(endpoint); err == nil {
					networkAvailable = true
					_ = response.Body.Close()
				}
			}
			data, _ := json.Marshal(map[string]any{
				"declared":  os.Getenv("MCP_DECLARED_TOKEN"),
				"inherited": os.Getenv("UNDECLARED_MCP_SECRET"),
				"outside": string(
					outside,
				),
				"host_proc_visible": hostProcErr == nil,
				"workspace_written": writeErr == nil,
				"arguments":         string(request.Params.Arguments),
				"network_available": networkAvailable,
			})
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}}}, nil
		})
	server.AddTool(&sdkmcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			log, err := os.OpenFile("action-started", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			_, _ = log.WriteString("once\n")
			_ = log.Close()
			child := exec.Command(
				"setsid",
				"sh",
				"-c",
				"i=0; while :; do i=$((i+1)); printf '%s' \"$i\" > heartbeat; sleep 0.03; done",
			)
			if err := child.Start(); err != nil {
				return nil, err
			}
			// Deliberately ignores protocol cancellation to exercise OS containment.
			time.Sleep(time.Hour)
			return &sdkmcp.CallToolResult{}, nil
		})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(0)
}

func TestLocalMCPCancellationAndTimeoutStopDetachedChildrenAndNextCallReconnects(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			cfg, workspace := isolatedMCPConfig(t)
			manager := NewManager()
			t.Cleanup(func() { require.NoError(t, manager.Close()) })
			require.NoError(t, manager.LoadFromConfig(t.Context(), cfg))
			ctx, cancel := context.WithCancel(t.Context())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := manager.CallTool(ctx, "local", "wait", nil)
				done <- err
			}()
			heartbeat := filepath.Join(workspace, "heartbeat")
			require.Eventually(
				t,
				func() bool { data, _ := os.ReadFile(heartbeat); return len(data) > 0 },
				time.Second,
				10*time.Millisecond,
			)
			if !timeout {
				cancel()
			}
			select {
			case err := <-done:
				if timeout {
					require.True(t, errors.Is(err, context.DeadlineExceeded), err)
				} else {
					require.True(t, errors.Is(err, context.Canceled), err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled MCP request did not return")
			}
			// A detached setsid child cannot survive the sandbox PID namespace's init.
			assertHeartbeatStopped(t, heartbeat)
			result, err := manager.CallTool(t.Context(), "local", "probe", nil)
			require.NoError(t, err)
			require.NotEmpty(t, result.Content)
			executions, err := os.ReadFile(filepath.Join(workspace, "action-started"))
			require.NoError(t, err)
			require.Equal(t, "once\n", string(executions), "canceled tool was replayed during reconnection")
		})
	}
}

func TestLocalMCPCancellationInterruptsConcurrentCallAndReconnectsOnce(t *testing.T) {
	cfg, workspace := isolatedMCPConfig(t)
	manager := NewManager()
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.LoadFromConfig(t.Context(), cfg))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled, interrupted := make(chan error, 1), make(chan error, 1)
	go func() { _, err := manager.CallTool(ctx, "local", "wait", nil); canceled <- err }()
	go func() { _, err := manager.CallTool(t.Context(), "local", "wait", nil); interrupted <- err }()
	actions := filepath.Join(workspace, "action-started")
	require.Eventually(
		t,
		func() bool { data, _ := os.ReadFile(actions); return string(data) == "once\nonce\n" },
		time.Second,
		10*time.Millisecond,
	)
	cancel()
	select {
	case err := <-canceled:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled request remained active")
	}
	select {
	case err := <-interrupted:
		require.ErrorContains(t, err, "interrupted")
	case <-time.After(time.Second):
		t.Fatal("concurrent interrupted request remained active")
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := manager.CallTool(t.Context(), "local", "probe", nil); results <- err }()
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, <-results)
	}
	starts, err := os.ReadFile(filepath.Join(workspace, "server-started"))
	require.NoError(t, err)
	require.Equal(t, "started\nstarted\n", string(starts), "independent concurrent calls should share one reconnection")
	executions, err := os.ReadFile(actions)
	require.NoError(t, err)
	require.Equal(t, "once\nonce\n", string(executions), "interrupted tools were replayed")
}

func isolatedMCPConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("real Linux isolation requires bubblewrap")
	}
	workspace, outside := t.TempDir(), t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	source, err := os.Open(self)
	require.NoError(t, err)
	defer source.Close()
	executable := filepath.Join(workspace, "mcp-fixture")
	target, err := os.OpenFile(executable, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	require.NoError(t, err)
	_, err = io.Copy(target, source)
	require.NoError(t, err)
	require.NoError(t, target.Close())
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("outside-secret"), 0o600))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(workspace, "server.env"), []byte("MCP_DECLARED_TOKEN=file-value\n"), 0o600),
	)
	t.Setenv("UNDECLARED_MCP_SECRET", "inherited-secret")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Isolation.Enabled = true
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"local": {
				Enabled: true,
				Command: executable,
				Args:    []string{"-test.run=^TestIsolationMCPServerProcess$", secret},
				EnvFile: "server.env",
				Env: map[string]string{
					"IVO_MCP_CHILD": "1", "MCP_WORKSPACE": workspace, "MCP_OUTSIDE_FILE": secret,
					"MCP_HOST_PID": strconv.Itoa(os.Getpid()), "MCP_DECLARED_TOKEN": "declared-value",
				},
			},
		},
	}
	return cfg, workspace
}

func TestLocalMCPNetworkAndDisabledIsolationPreserveDeclaredPolicy(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("available"))
	}))
	defer endpoint.Close()
	for _, tc := range []struct {
		name       string
		enabled    bool
		networkOff bool
	}{
		{"isolated default network", true, false},
		{"isolated offline", true, true},
		{"ordinary process", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := isolatedMCPConfig(t)
			cfg.Isolation.Enabled = tc.enabled
			cfg.Isolation.DisableNetwork = tc.networkOff
			cfg.Tools.MCP.Servers["local"].Env["MCP_NETWORK_ENDPOINT"] = endpoint.URL
			manager := NewManager()
			t.Cleanup(func() { require.NoError(t, manager.Close()) })
			require.NoError(t, manager.LoadFromConfig(t.Context(), cfg))
			result, err := manager.CallTool(t.Context(), "local", "probe", nil)
			require.NoError(t, err)
			var data map[string]any
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &data))
			require.Equal(t, !tc.networkOff, data["network_available"])
			if !tc.enabled {
				require.Equal(t, "inherited-secret", data["inherited"])
				require.Equal(t, "outside-secret", data["outside"])
			}
		})
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestLocalMCPStartupTimeoutStopsItsProcessNamespace(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"unresponsive": {
				Enabled: true,
				Command: "/bin/sh",
				Args: []string{
					"-c",
					"setsid sh -c 'i=0; while :; do i=$((i+1)); printf \"%s\" \"$i\" > heartbeat; sleep 0.03; done' & wait",
				},
			},
		},
	}
	manager := NewManager()
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := manager.LoadFromConfig(ctx, cfg)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	assertHeartbeatStopped(t, filepath.Join(workspace, "heartbeat"))
}

func TestLocalMCPMissingSandboxFailsBeforeStartingServer(t *testing.T) {
	cfg, workspace := isolatedMCPConfig(t)
	t.Setenv("PATH", t.TempDir())
	manager := NewManager()
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	err := manager.LoadFromConfig(t.Context(), cfg)
	require.ErrorContains(t, err, "bubblewrap")
	require.ErrorContains(t, err, "not retried without isolation")
	_, err = os.Stat(filepath.Join(workspace, "server-started"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLocalMCPCloseInterruptsInflightCallAndStopsDescendants(t *testing.T) {
	cfg, workspace := isolatedMCPConfig(t)
	manager := NewManager()
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.LoadFromConfig(t.Context(), cfg))
	done := make(chan error, 1)
	go func() { _, err := manager.CallTool(t.Context(), "local", "wait", nil); done <- err }()
	heartbeat := filepath.Join(workspace, "heartbeat")
	require.Eventually(
		t,
		func() bool { data, _ := os.ReadFile(heartbeat); return len(data) > 0 },
		time.Second,
		10*time.Millisecond,
	)
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("manager Close did not release the in-flight tool")
	}
	require.ErrorIs(t, <-done, context.Canceled)
	assertHeartbeatStopped(t, heartbeat)
}

func assertHeartbeatStopped(t *testing.T, heartbeat string) {
	t.Helper()
	time.Sleep(80 * time.Millisecond)
	before, err := os.ReadFile(heartbeat)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "detached child survived termination")
}

func TestLocalMCPIsolationFiltersEnvironmentAndKeepsDeclaredVariables(t *testing.T) {
	cfg, workspace := isolatedMCPConfig(t)
	manager := NewManager()
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	startupCtx, finishStartup := context.WithCancel(t.Context())
	require.NoError(t, manager.LoadFromConfig(startupCtx, cfg))
	finishStartup()
	result, err := manager.CallTool(t.Context(), "local", "probe", nil)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &data))
	require.Equal(t, "declared-value", data["declared"])
	require.Equal(t, "", data["inherited"])
	require.Equal(t, "", data["outside"])
	require.Equal(t, false, data["host_proc_visible"])
	require.Equal(t, true, data["workspace_written"])
	require.Equal(t, "{}", data["arguments"])
	contents, err := os.ReadFile(filepath.Join(workspace, "workspace-output"))
	require.NoError(t, err)
	require.Equal(t, "written", string(contents))
}
