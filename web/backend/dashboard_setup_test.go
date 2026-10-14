package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardSetupCodeIsPrivateAndRotates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "launcher-auth.db")
	first, path, err := createDashboardSetupToken(dbPath)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, first, strings.TrimSpace(string(contents)))
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	second, secondPath, err := createDashboardSetupToken(dbPath)
	require.NoError(t, err)
	require.Equal(t, path, secondPath)
	require.NotEqual(t, first, second)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))
	token, _, err := createDashboardSetupToken(filepath.Join(blocked, "auth.db"))
	require.Error(t, err)
	require.Empty(t, token, "an unpublished setup capability must not be enabled")
}
