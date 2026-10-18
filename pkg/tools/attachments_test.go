package tools

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/media"
)

func TestAttachmentStorePreservesOriginalsAndDecodesText(t *testing.T) {
	scope := RequestScope{AgentID: "main", SessionKey: "agent:main:telegram:direct:42"}
	for _, tc := range []struct {
		name string
		raw  []byte
		text string
		read bool
	}{
		{"source.go", []byte("package main\n// Привет\n"), "package main\n// Привет\n", true},
		{"utf16.txt", []byte{0xff, 0xfe, 'h', 0, 'i', 0}, "hi", true},
		{"report.pdf", []byte("%PDF-1.4 binary document"), "", false},
		{"invalid.txt", []byte{0xff, 0x00, 0xfe}, "", false},
		{"empty.txt", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewAttachmentStore(workspace)
			path := filepath.Join(t.TempDir(), "download.bin")
			require.NoError(t, os.WriteFile(path, tc.raw, 0o600))
			ref, err := store.Import(scope, path, media.MediaMeta{Filename: tc.name}, 1024)
			require.NoError(t, err)
			id := strings.TrimPrefix(ref, AttachmentPrefix)
			raw, err := os.ReadFile(filepath.Join(workspace, ".attachments", id+".original"))
			require.NoError(t, err)
			require.Equal(t, string(tc.raw), string(raw))
			repeated, err := store.Import(scope, path, media.MediaMeta{Filename: tc.name}, 1024)
			require.NoError(t, err)
			require.Equal(t, ref, repeated)
			require.NoError(t, os.Remove(path))
			// A fresh instance must work without any channel media mappings.
			restarted := NewAttachmentStore(workspace)
			info, err := restarted.Info(scope, ref)
			require.NoError(t, err)
			require.Equal(t, tc.name, info.Filename)
			require.Equal(t, tc.read, info.Text)
			file, err := restarted.Open(scope, ref)
			if !tc.read {
				require.ErrorContains(t, err, "extractor")
				return
			}
			require.NoError(t, err)
			defer file.Close()
			text, err := io.ReadAll(file)
			require.NoError(t, err)
			require.Equal(t, tc.text, string(text))
		})
	}
}

func TestAttachmentReadsAreScopedAndPaginated(t *testing.T) {
	workspace := t.TempDir()
	store := NewAttachmentStore(workspace)
	scope := RequestScope{AgentID: "main", SessionKey: "agent:main:telegram:direct:42"}
	path := filepath.Join(t.TempDir(), "source.go")
	require.NoError(t, os.WriteFile(path, []byte("abcdefgh"), 0o600))
	ref, err := store.Import(scope, path, media.MediaMeta{Filename: "source.go"}, 1024)
	require.NoError(t, err)
	read := NewReadFileTool(workspace, true, 4).WithAttachments(store)
	ctx := WithRequestScope(t.Context(), scope)
	first := read.Execute(ctx, map[string]any{"path": ref})
	require.False(t, first.IsError, first.ForLLM)
	require.Contains(t, first.ForLLM, "abcd")
	require.Contains(t, first.ForLLM, "offset=4")
	last := read.Execute(ctx, map[string]any{"path": ref, "offset": 4})
	require.False(t, last.IsError, last.ForLLM)
	require.Contains(t, last.ForLLM, "efgh")
	require.Contains(t, last.ForLLM, "END OF FILE")
	for _, denied := range []RequestScope{
		{},
		{AgentID: "other", SessionKey: scope.SessionKey},
		{AgentID: scope.AgentID, SessionKey: "foreign"},
		{AgentID: scope.AgentID, SessionKey: scope.SessionKey, Inline: true},
		{AgentID: scope.AgentID, SessionKey: scope.SessionKey, NoHistory: true},
	} {
		result := read.Execute(WithRequestScope(t.Context(), denied), map[string]any{"path": ref})
		require.True(t, result.IsError)
		require.NotContains(t, result.ForLLM, "abcd")
	}
	rawPath := filepath.Join(workspace, ".attachments", strings.TrimPrefix(ref, AttachmentPrefix)+".original")
	require.True(t, read.Execute(ctx, map[string]any{"path": rawPath}).IsError)
	_, err = store.Import(scope, path, media.MediaMeta{}, 4)
	require.ErrorContains(t, err, "size limit")
	_, err = store.Open(scope, AttachmentPrefix+"../../private.txt")
	require.ErrorContains(t, err, "invalid attachment")
}
