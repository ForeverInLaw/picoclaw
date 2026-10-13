//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecWorkspaceGuardAcceptsSchemelessURL(t *testing.T) {
	for _, target := range []string{"example.com/path", "wttr.in/Beijing?T", "api.example.com/v1/search?q=weather", "https://example.com/path"} {
		t.Run(target, func(t *testing.T) {
			tool, err := NewExecTool(t.TempDir(), true)
			require.NoError(t, err)
			result := tool.Execute(t.Context(), map[string]any{"command": "printf '%s' '" + target + "'"})
			require.False(t, result.IsError, result.ForLLM)
			require.Equal(t, target, result.ForLLM)
		})
	}
}

func TestExecWorkspaceGuardRetainsFilesystemChecks(t *testing.T) {
	workspace := t.TempDir()
	tool, err := NewExecTool(workspace, true)
	require.NoError(t, err)
	for _, target := range []string{
		"/etc/passwd",
		"../outside.txt",
		"file:///etc/passwd",
		"script.py/output",
		"example.com/../outside",
	} {
		t.Run(target, func(t *testing.T) {
			result := tool.Execute(t.Context(), map[string]any{"command": "printf '%s' '" + target + "'"})
			require.True(t, result.IsError)
			require.Contains(t, result.ForLLM, "Command blocked by safety guard")
		})
	}
	inside := filepath.Join(workspace, "inside.txt")
	for _, target := range []string{inside, "file://" + inside} {
		result := tool.Execute(t.Context(), map[string]any{"command": "printf '%s' '" + target + "'"})
		require.False(t, result.IsError, result.ForLLM)
		require.Equal(t, target, result.ForLLM)
	}
	result := tool.Execute(t.Context(), map[string]any{
		"command": "printf '%s' 'example.com/etc/passwd'; printf '%s' '/etc/passwd'",
	})
	require.True(t, result.IsError, "a URL exemption must not extend to a separate filesystem path")
	require.Contains(t, result.ForLLM, "path outside working dir")
}

func TestExecWorkspaceGuardDoesNotExemptExistingDomainNamedPaths(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "example.com"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(workspace, "outside.net")))
	tool, err := NewExecTool(workspace, true)
	require.NoError(t, err)
	for _, target := range []string{"example.com/path", "outside.net/path"} {
		result := tool.Execute(t.Context(), map[string]any{"command": "printf '%s' '" + target + "'"})
		require.True(t, result.IsError, "existing local directories/symlinks must retain filesystem validation")
		require.Contains(t, result.ForLLM, "path outside working dir")
	}
}

func TestExecWorkspaceGuardDisabledKeepsExistingBehavior(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	require.NoError(t, err)
	for _, target := range []string{"example.com/path", "/etc/passwd", "../outside.txt", "file:///etc/passwd"} {
		result := tool.Execute(t.Context(), map[string]any{"command": "printf '%s' '" + target + "'"})
		require.False(t, result.IsError, result.ForLLM)
		require.Equal(t, target, result.ForLLM)
	}
}
