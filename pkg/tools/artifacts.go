package tools

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const artifactPrefix = "artifact://mcp/"

// ArtifactStore owns raw MCP records. Authorization uses persisted base scope,
// while its filtered file view plugs into read_file's existing pagination.
type ArtifactStore struct {
	directory    string
	inlineRunes  int
	previewRunes int
	quota        int64
	filter       func(string) string
	mu           sync.Mutex
}

type artifactRecord struct {
	Version    int    `json:"version"`
	AgentID    string `json:"agent_id"`
	SessionKey string `json:"session_key"`
	Text       string `json:"text"`
}

func NewArtifactStore(settings config.MCPArtifactConfig, filter func(string) string) *ArtifactStore {
	if settings.Directory == "" {
		settings.Directory = config.DefaultConfig().Tools.MCP.Artifacts.Directory
	}
	directory, _ := filepath.Abs(settings.Directory)
	if settings.MaxInlineRunes <= 0 {
		settings.MaxInlineRunes = 16 * 1024
	}
	if settings.PreviewRunes <= 0 {
		settings.PreviewRunes = 1000
	}
	if filter == nil {
		filter = func(text string) string { return text }
	}
	return &ArtifactStore{
		directory: directory, inlineRunes: settings.MaxInlineRunes,
		previewRunes: min(settings.PreviewRunes, 4096, settings.MaxInlineRunes),
		quota:        settings.MaxDiskBytes, filter: filter,
	}
}

func validArtifactScope(scope RequestScope) bool {
	return scope.AgentID != "" && scope.SessionKey != "" && !scope.Inline && !scope.NoHistory
}

func (s *ArtifactStore) root(create bool) (*os.Root, error) {
	return openPrivateRoot(s.directory, create)
}

func openPrivateRoot(directory string, create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("retained file directory must be a real directory")
	}
	return os.OpenRoot(directory)
}

func (s *ArtifactStore) persist(scope RequestScope, text string) (string, error) {
	if !validArtifactScope(scope) {
		return "", fmt.Errorf("authorized agent and conversation are required")
	}
	encoded, err := json.Marshal(
		artifactRecord{Version: 1, AgentID: scope.AgentID, SessionKey: scope.SessionKey, Text: text},
	)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if s.quota < 0 {
		return "", fmt.Errorf("artifact max_disk_bytes must be >= 0")
	}
	if s.quota > 0 {
		// Separate gateway/CLI instances must check quota and publish atomically
		// under the same OS lock. Contention fails explicitly after completion.
		lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return "", err
		}
		defer lock.Close()
		if lockErr := lockArtifactFile(lock); lockErr != nil {
			return "", fmt.Errorf("artifact storage is busy or quota locking is unavailable: %w", lockErr)
		}
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return "", err
		}
		remaining := s.quota - int64(len(encoded))
		quotaError := fmt.Errorf(
			"artifact disk quota exceeded; increase max_disk_bytes or explicitly clean retained artifacts",
		)
		if remaining < 0 {
			return "", quotaError
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return "", err
			}
			if info.Mode().IsRegular() {
				if remaining < info.Size() {
					return "", quotaError
				}
				remaining -= info.Size()
			}
		}
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	name := hex.EncodeToString(id)
	if err := fileutil.WriteFileAtomicInRoot(root, name+".json", encoded, 0o600); err != nil {
		return "", err
	}
	return artifactPrefix + name, nil
}

// Capture changes only model context for a normal result. Storage failure is a
// terminal completion notice: the external operation already ran and cannot be replayed.
func (s *ArtifactStore) Capture(scope RequestScope, result *ToolResult, raw string) *ToolResult {
	if s == nil || utf8.RuneCountInString(raw) <= s.inlineRunes {
		return result
	}
	ref, err := s.persist(scope, raw)
	if err != nil {
		message := s.filter(
			"MCP operation completed, but its large result could not be stored: " + err.Error() + ". Do not repeat the operation; fix artifact storage for subsequent calls.",
		)
		return &ToolResult{ForLLM: message, ForUser: message, Terminal: true, Err: err}
	}
	preview := []rune(s.filter(raw))
	if len(preview) > s.previewRunes {
		preview = preview[:s.previewRunes]
	}
	captured := *result
	status := "MCP result"
	if result.IsError {
		status = "MCP returned error"
	}
	captured.ForLLM = fmt.Sprintf(
		"[%s saved: %s | %d characters. Use read_file with this reference and offset/length for details.]\n\n%s",
		status,
		ref,
		utf8.RuneCountInString(raw),
		string(preview),
	)
	return &captured
}

// Open authorizes the persisted owner before exposing a fully filtered view.
// Offsets apply to this view; raw bytes never cross a partial-read boundary.
func (s *ArtifactStore) Open(scope RequestScope, reference string) (fs.File, error) {
	if !validArtifactScope(scope) {
		return nil, fmt.Errorf("artifact access denied: authorized conversation required")
	}
	id := strings.TrimPrefix(reference, artifactPrefix)
	decoded, err := hex.DecodeString(id)
	if !strings.HasPrefix(reference, artifactPrefix) || err != nil || len(decoded) != 16 ||
		hex.EncodeToString(decoded) != id {
		return nil, fmt.Errorf("invalid artifact reference")
	}
	root, err := s.root(false)
	if err != nil {
		return nil, fmt.Errorf("artifact unavailable: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(id + ".json")
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact unavailable or unsafe")
	}
	encoded, err := root.ReadFile(id + ".json")
	if err != nil {
		return nil, fmt.Errorf("artifact unavailable: %w", err)
	}
	var record artifactRecord
	if json.Unmarshal(encoded, &record) != nil || record.Version != 1 {
		return nil, fmt.Errorf("invalid artifact record")
	}
	if record.AgentID != scope.AgentID || record.SessionKey != scope.SessionKey {
		return nil, fmt.Errorf("artifact access denied: another agent or conversation")
	}
	view := []byte(s.filter(record.Text))
	return &artifactFile{Reader: bytes.NewReader(view), name: reference}, nil
}

// ProtectPath keeps physical records out of the ordinary filesystem route,
// including absolute paths, allowed paths and symlink aliases.
func (s *ArtifactStore) ProtectPath(path string) bool {
	if s == nil {
		return false
	}
	return protectStoredPath(path, s.directory)
}

func protectStoredPath(path, directory string) bool {
	path, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	root := directory
	if isWithinWorkspace(path, root) {
		return true
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && isWithinWorkspace(resolved, root)
}

type artifactFile struct {
	*bytes.Reader
	name string
}

func (f *artifactFile) Close() error               { return nil }
func (f *artifactFile) Stat() (fs.FileInfo, error) { return artifactInfo{f.name, f.Size()}, nil }

type artifactInfo struct {
	name string
	size int64
}

func (f artifactInfo) Name() string       { return f.name }
func (f artifactInfo) Size() int64        { return f.size }
func (f artifactInfo) Mode() fs.FileMode  { return 0o600 }
func (f artifactInfo) ModTime() time.Time { return time.Time{} }
func (f artifactInfo) IsDir() bool        { return false }
func (f artifactInfo) Sys() any           { return nil }
