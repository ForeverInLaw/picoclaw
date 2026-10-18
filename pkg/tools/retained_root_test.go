package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

func TestRetainedRootRejectsExistingWritableDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe Windows ACLs")
	}
	for _, mode := range []os.FileMode{0o770, 0o777} {
		for _, create := range []bool{false, true} {
			directory := filepath.Join(t.TempDir(), "retained")
			require.NoError(t, os.Mkdir(directory, 0o700))
			require.NoError(t, os.Chmod(directory, mode))
			root, err := openPrivateRoot(directory, create)
			if root != nil {
				root.Close()
			}
			require.ErrorContains(t, err, "writable")
		}
	}
}

func TestRetainedRootPreservesPrivateAndReadOnlyDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe Windows ACLs")
	}
	for _, mode := range []os.FileMode{0o700, 0o500} {
		directory := t.TempDir()
		t.Cleanup(func() { require.NoError(t, os.Chmod(directory, 0o700)) })
		require.NoError(t, os.WriteFile(filepath.Join(directory, "record"), []byte("retained"), 0o600))
		require.NoError(t, os.Chmod(directory, mode))
		for _, create := range []bool{false, true} {
			root, err := openPrivateRoot(directory, create)
			require.NoError(t, err)
			data, err := root.ReadFile("record")
			root.Close()
			require.NoError(t, err)
			require.Equal(t, "retained", string(data))
			info, err := os.Stat(directory)
			require.NoError(t, err)
			require.Equal(t, mode, info.Mode().Perm())
		}
	}
}

func TestRetainedRootRejectsForeignOwner(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		t.Skip("Changing directory ownership requires Unix root privileges")
	}
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o755))
	require.NoError(t, os.Chown(directory, os.Geteuid()+1, -1))
	for _, create := range []bool{false, true} {
		root, err := openPrivateRoot(directory, create)
		if root != nil {
			root.Close()
		}
		require.ErrorContains(t, err, "owned by")
	}
}

func TestRetainedRootRejectsSymlinkDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Creating symlinks requires privileges on Windows")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(t.TempDir(), alias))
	for _, create := range []bool{false, true} {
		root, err := openPrivateRoot(alias, create)
		if root != nil {
			root.Close()
		}
		require.ErrorContains(t, err, "real directory")
	}
}

func TestWritableRetainedRootCannotAuthorizeReplacedMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe Windows ACLs")
	}
	workspace := t.TempDir()
	store := NewAttachmentStore(workspace)
	owner := RequestScope{AgentID: "main", SessionKey: "owner"}
	source := filepath.Join(t.TempDir(), "source.go")
	require.NoError(t, os.WriteFile(source, []byte("victim source"), 0o600))
	ref, err := store.Import(owner, source, media.MediaMeta{Filename: "source.go"}, 1024)
	require.NoError(t, err)
	directory := filepath.Join(workspace, ".attachments")
	require.NoError(t, os.Chmod(directory, 0o777))
	manifest := filepath.Join(directory, strings.TrimPrefix(ref, AttachmentPrefix)+".json")
	encoded, err := os.ReadFile(manifest)
	require.NoError(t, err)
	var record attachmentRecord
	require.NoError(t, json.Unmarshal(encoded, &record))
	record.SessionKey = "foreign"
	encoded, err = json.Marshal(record)
	require.NoError(t, err)
	// A writable containing directory permits unlink/replacement despite 0600 files.
	require.NoError(t, os.Remove(manifest))
	require.NoError(t, os.WriteFile(manifest, encoded, 0o600))
	reopened := NewAttachmentStore(workspace)
	foreign := RequestScope{AgentID: "main", SessionKey: "foreign"}
	_, err = reopened.Info(foreign, ref)
	require.ErrorContains(t, err, "writable")
	file, err := reopened.Open(foreign, ref)
	if file != nil {
		file.Close()
	}
	require.ErrorContains(t, err, "writable")
}

func TestMCPArtifactWritableRootRejectsReadsAndCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe Windows ACLs")
	}
	directory := t.TempDir()
	store := NewArtifactStore(config.MCPArtifactConfig{Directory: directory, MaxInlineRunes: 1}, nil)
	scope := RequestScope{AgentID: "main", SessionKey: "owner"}
	ref, err := store.persist(scope, "original result")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o777))
	file, err := store.Open(scope, ref)
	if file != nil {
		file.Close()
	}
	require.ErrorContains(t, err, "writable")
	result := store.Capture(scope, NewToolResult("completed"), "completed")
	require.True(t, result.Terminal)
	require.ErrorContains(t, result.Err, "writable")
	require.Contains(t, result.ForLLM, "Do not repeat")
}
