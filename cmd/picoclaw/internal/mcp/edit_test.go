package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestMCPConfiguredEditorProcess(t *testing.T) {
	if os.Getenv("MCP_FAKE_EDITOR_CHILD") != "1" {
		return
	}
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte(os.Getenv("MCP_FAKE_EDITOR_DOCUMENT")), 0o600); err != nil {
		os.Exit(2)
	}
	fmt.Println("configured editor completed")
	os.Exit(0)
}

func TestMCPEditValidatesSecureTemporaryDocumentBeforeSavingOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	t.Setenv("VISUAL", `fake-editor --wait`)
	t.Setenv("EDITOR", "ignored-editor")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.MemoryIndex.Enabled = true
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "edited", Model: "openai/test", APIKeys: []string{"first-editor-key", "second-editor-key"}},
	}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{"old": {Enabled: false, Command: "old"}}
	require.NoError(t, config.SaveConfig(path, cfg))
	securityPath := filepath.Join(filepath.Dir(path), config.SecurityConfigFile)
	require.NoError(
		t,
		os.WriteFile(
			securityPath,
			[]byte(
				"model_list:\n  edited:\n    api_keys: [first-editor-key, second-editor-key]\nskills:\n  clawhub:\n    auth_token: hub-editor-secret\n",
			),
			0o600,
		),
	)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	old := editorCommand
	defer func() { editorCommand = old }()
	var tempPath string
	var document string
	var concurrent bool
	editorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		require.Equal(t, "fake-editor", name)
		require.Equal(t, "--wait", args[0])
		tempPath = args[len(args)-1]
		require.NotEqual(t, path, tempPath)
		info, statErr := os.Stat(tempPath)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		edited := document
		if edited == "" {
			prepared, readErr := os.ReadFile(tempPath)
			require.NoError(t, readErr)
			edited = string(
				bytes.ReplaceAll(prepared, []byte("\"command\": \"old\""), []byte("\"command\": \"updated\"")),
			)
		}
		if concurrent {
			require.NoError(
				t,
				os.WriteFile(securityPath, []byte("skills:\n  clawhub:\n    auth_token: changed-secret\n"), 0o600),
			)
		}
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPConfiguredEditorProcess$", "--", tempPath)
		child.Env = append(os.Environ(), "MCP_FAKE_EDITOR_CHILD=1", "MCP_FAKE_EDITOR_DOCUMENT="+edited)
		return child
	}
	for _, invalid := range []string{`{"secret": "editor-secret"`, `{"tools":{"mcp":{"servers":{"bad":{"enabled":true,"type":"http","url":"not-a-url-editor-secret"}}}}}`} {
		document = invalid
		cmd := NewMCPCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs([]string{"edit"})
		err = cmd.ExecuteContext(t.Context())
		require.Error(t, err)
		require.NotContains(t, output.String()+err.Error(), "editor-secret")
		retained, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, original, retained)
		_, statErr := os.Stat(tempPath)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	}
	document = ""
	cmd := NewMCPCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"edit"})
	require.NoError(t, cmd.ExecuteContext(t.Context()), output.String())
	require.Contains(t, output.String(), "configured editor completed")
	reopened, err := config.LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "updated", reopened.Tools.MCP.Servers["old"].Command)
	require.Equal(t, "legacy", reopened.Agents.Defaults.ContextManager)
	require.True(t, reopened.Agents.Defaults.MemoryIndex.Enabled)
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(source), "\"api_keys\"")
	require.Contains(t, string(source), "second-editor-key")
	security, err := os.ReadFile(securityPath)
	require.NoError(t, err)
	require.Contains(t, string(security), "second-editor-key")
	require.Contains(t, string(security), "hub-editor-secret")
	_, err = os.Stat(tempPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	concurrent = true
	cmd = NewMCPCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"edit"})
	require.ErrorContains(t, cmd.ExecuteContext(t.Context()), "changed while editing")
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, source, retained)
	security, err = os.ReadFile(securityPath)
	require.NoError(t, err)
	require.Contains(t, string(security), "changed-secret")
}
