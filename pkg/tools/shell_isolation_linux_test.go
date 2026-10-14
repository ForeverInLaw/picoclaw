//go:build linux

package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func requireBubblewrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("real Linux isolation requires bubblewrap")
	}
}

func TestShellIsolationKeepsMountsAndEnvironmentExplicit(t *testing.T) {
	requireBubblewrap(t)
	workspace, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("host-secret"), 0o600))
	t.Setenv("UNDECLARED_TEST_SECRET", "hidden-value")
	t.Setenv("DECLARED_TEST_VALUE", "allowed-value")
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.Exec.EnableDenyPatterns = false
	cfg.Isolation = config.IsolationConfig{Enabled: true, EnvAllowlist: []string{"DECLARED_TEST_VALUE"}}
	tool, err := NewExecToolWithConfig(workspace, false, cfg)
	require.NoError(t, err)
	command := "printf workspace > inside.txt; " +
		"if cat '" + filepath.Join(outside, "secret") + "'; then printf outside-readable; fi; " +
		"if printf forbidden > '" + filepath.Join(outside, "created") + "'; then printf outside-writable; fi; " +
		fmt.Sprintf("if [ -e /proc/%d/environ ]; then printf host-proc-visible; fi; ", os.Getpid()) +
		"if [ -w /usr ]; then printf runtime-writable; fi; " +
		"printf '%s|%s' \"$DECLARED_TEST_VALUE\" \"$UNDECLARED_TEST_SECRET\""
	result := tool.Execute(t.Context(), map[string]any{"command": command})
	require.False(t, result.IsError, result.ForLLM)
	data, err := os.ReadFile(filepath.Join(workspace, "inside.txt"))
	require.NoError(t, err)
	require.Equal(t, "workspace", string(data))
	require.NotContains(t, result.ForLLM, "host-secret")
	require.NotContains(t, result.ForLLM, "outside-readable")
	require.NotContains(t, result.ForLLM, "outside-writable")
	require.Contains(t, result.ForLLM, "allowed-value|")
	require.NotContains(t, result.ForLLM, "hidden-value")
	require.NotContains(t, result.ForLLM, "host-proc-visible")
	require.NotContains(t, result.ForLLM, "runtime-writable")
	_, err = os.Stat(filepath.Join(outside, "created"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func isolationShell(t *testing.T, policy config.IsolationConfig) (*ExecTool, string) {
	t.Helper()
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.Exec.EnableDenyPatterns = false
	cfg.Isolation = policy
	tool, err := NewExecToolWithConfig(workspace, false, cfg)
	require.NoError(t, err)
	return tool, workspace
}

func TestShellIsolationExtraMountsHaveOnlyDeclaredAccess(t *testing.T) {
	requireBubblewrap(t)
	readOnly, writable := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(readOnly, "input"), []byte("readable-input"), 0o600))
	tool, _ := isolationShell(t, config.IsolationConfig{Enabled: true, ExposePaths: []config.ExposePath{
		{Source: readOnly, Target: "/mnt/input", Mode: "ro"},
		{Source: writable, Target: "/mnt/output", Mode: "rw"},
	}})
	result := tool.Execute(
		t.Context(),
		map[string]any{
			"command": "cat /mnt/input/input; if printf changed > /mnt/input/input; then printf ro-overwritten; fi; printf saved > /mnt/output/result",
		},
	)
	require.False(t, result.IsError, result.ForLLM)
	require.Contains(t, result.ForLLM, "readable-input")
	require.NotContains(t, result.ForLLM, "ro-overwritten")
	original, err := os.ReadFile(filepath.Join(readOnly, "input"))
	require.NoError(t, err)
	require.Equal(t, "readable-input", string(original))
	written, err := os.ReadFile(filepath.Join(writable, "result"))
	require.NoError(t, err)
	require.Equal(t, "saved", string(written))
}

func TestShellIsolationNetworkIsEnabledUnlessDisabled(t *testing.T) {
	requireBubblewrap(t)
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("network-available"))
	}))
	defer endpoint.Close()
	for _, disabled := range []bool{false, true} {
		tool, _ := isolationShell(t, config.IsolationConfig{Enabled: true, DisableNetwork: disabled})
		command := fmt.Sprintf(
			"/usr/bin/python3 -c 'import urllib.request; print(urllib.request.urlopen(\"%s\", timeout=0.2).read().decode())'",
			endpoint.URL,
		)
		result := tool.Execute(t.Context(), map[string]any{"command": command})
		require.Equal(t, disabled, result.IsError, result.ForLLM)
		if !disabled {
			require.Contains(t, result.ForLLM, "network-available")
		}
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestShellIsolationUnavailableOrDeniedNeverFallsBack(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		t.Run(fmt.Sprintf("unavailable=%t", unavailable), func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.Symlink("/bin/sh", filepath.Join(bin, "sh")))
			if !unavailable {
				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(bin, "bwrap"),
						[]byte("#!/bin/sh\nprintf 'bwrap: namespace creation denied by fixture' >&2\nexit 23\n"),
						0o700,
					),
				)
			}
			t.Setenv("PATH", bin)
			tool, workspace := isolationShell(t, config.IsolationConfig{Enabled: true})
			result := tool.Execute(t.Context(), map[string]any{"command": "printf unsandboxed > escaped"})
			require.True(t, result.IsError)
			require.Contains(t, result.ForLLM, "bubblewrap")
			_, err := os.Stat(filepath.Join(workspace, "escaped"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestShellIsolationCancellationAndTimeoutKillDetachedChildren(t *testing.T) {
	requireBubblewrap(t)
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			tool, workspace := isolationShell(t, config.IsolationConfig{Enabled: true})
			if timeout {
				tool.SetTimeout(500 * time.Millisecond)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *ToolResult, 1)
			go func() {
				done <- tool.Execute(ctx, map[string]any{"command": `setsid sh -c 'i=0; while :; do i=$((i+1)); printf "%s" "$i" > heartbeat; sleep 0.03; done' & wait`})
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
			case result := <-done:
				require.True(t, result.IsError)
				if timeout {
					require.Contains(t, result.ForLLM, "timed out")
				}
			case <-time.After(time.Second):
				t.Fatal("shell cancellation/timeout did not return")
			}
			time.Sleep(80 * time.Millisecond)
			before, err := os.ReadFile(heartbeat)
			require.NoError(t, err)
			time.Sleep(100 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "detached child survived")
		})
	}
}
