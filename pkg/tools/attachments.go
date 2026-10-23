package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/h2non/filetype"
	"golang.org/x/text/encoding/unicode"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/media"
)

const AttachmentPrefix = "attachment://"

var (
	ErrAttachmentUnavailable = errors.New("attachment source no longer available")
	ErrAttachmentTooLarge    = errors.New("attachment exceeds the configured media size limit")
)

// AttachmentStore retains originals and text views independently of the channel's
// temporary media cache. References are owned by the receiving conversation.
type AttachmentStore struct{ directory string }

type AttachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	TextSize    int64  `json:"text_size"`
	Text        bool   `json:"text"`
}

type attachmentRecord struct {
	Version    int    `json:"version"`
	AgentID    string `json:"agent_id"`
	SessionKey string `json:"session_key"`
	AttachmentInfo
}

func NewAttachmentStore(workspace string) *AttachmentStore {
	return &AttachmentStore{directory: filepath.Join(workspace, ".attachments")}
}

// Import bounds the read, preserves the original bytes, and publishes metadata
// last. Content-derived IDs make repeated resolution and concurrent imports idempotent.
func (s *AttachmentStore) Import(scope RequestScope, path string, meta media.MediaMeta, maxBytes int) (string, error) {
	if s == nil || !validArtifactScope(scope) {
		return "", fmt.Errorf("attachment access denied: authorized conversation required")
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %w", ErrAttachmentUnavailable, err)
		}
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || maxBytes <= 0 {
		return "", fmt.Errorf("attachment is not a regular file or has an invalid media size limit")
	}
	if info.Size() > int64(maxBytes) {
		return "", ErrAttachmentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		return "", ErrAttachmentTooLarge
	}
	name := filepath.Base(strings.ReplaceAll(meta.Filename, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = filepath.Base(path)
	}
	if len(name) > 255 {
		name = "attachment"
	}
	text, isText := attachmentText(data)
	record := attachmentRecord{
		Version: 1, AgentID: scope.AgentID, SessionKey: scope.SessionKey,
		AttachmentInfo: AttachmentInfo{
			Filename: name, ContentType: meta.ContentType, Size: int64(len(data)),
			Text: isText, TextSize: int64(len(text)),
		},
	}
	if record.ContentType == "" {
		record.ContentType = http.DetectContentType(data)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write(encoded)
	hash.Write(data)
	id := hex.EncodeToString(hash.Sum(nil))
	root, err := openPrivateRoot(s.directory, true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err = fileutil.WriteFileAtomicInRoot(root, id+".original", data, 0o600); err != nil {
		return "", err
	}
	if isText {
		if err = fileutil.WriteFileAtomicInRoot(root, id+".txt", text, 0o600); err != nil {
			return "", err
		}
	}
	if err = fileutil.WriteFileAtomicInRoot(root, id+".json", encoded, 0o600); err != nil {
		return "", err
	}
	return AttachmentPrefix + id, nil
}

func (s *AttachmentStore) record(scope RequestScope, reference string) (attachmentRecord, error) {
	var record attachmentRecord
	if s == nil || !validArtifactScope(scope) {
		return record, fmt.Errorf("attachment access denied: authorized conversation required")
	}
	id := strings.TrimPrefix(reference, AttachmentPrefix)
	decoded, err := hex.DecodeString(id)
	if !strings.HasPrefix(reference, AttachmentPrefix) || err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != id {
		return record, fmt.Errorf("invalid attachment reference")
	}
	root, err := openPrivateRoot(s.directory, false)
	if err != nil {
		return record, fmt.Errorf("attachment unavailable: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(id + ".json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return record, fmt.Errorf("attachment metadata unavailable or unsafe")
	}
	encoded, err := root.ReadFile(id + ".json")
	if err != nil {
		return record, err
	}
	if json.Unmarshal(encoded, &record) != nil || record.Version != 1 {
		return record, fmt.Errorf("invalid attachment record")
	}
	if record.AgentID != scope.AgentID || record.SessionKey != scope.SessionKey {
		return record, fmt.Errorf("attachment access denied: another agent or conversation")
	}
	return record, nil
}

func (s *AttachmentStore) Info(scope RequestScope, reference string) (AttachmentInfo, error) {
	record, err := s.record(scope, reference)
	return record.AttachmentInfo, err
}

// Open exposes only the decoded text view to read_file's existing pagination.
func (s *AttachmentStore) Open(scope RequestScope, reference string) (fs.File, error) {
	record, err := s.record(scope, reference)
	if err != nil {
		return nil, err
	}
	if !record.Text {
		return nil, fmt.Errorf("attachment format or encoding requires a document extractor; no text was extracted")
	}
	root, err := openPrivateRoot(s.directory, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := strings.TrimPrefix(reference, AttachmentPrefix) + ".txt"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment text unavailable or unsafe")
	}
	return root.Open(name)
}

// OpenOriginal authorizes access to retained binary input for tools such as image editing.
func (s *AttachmentStore) OpenOriginal(scope RequestScope, reference string) (fs.File, error) {
	if _, err := s.record(scope, reference); err != nil {
		return nil, err
	}
	root, err := openPrivateRoot(s.directory, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := strings.TrimPrefix(reference, AttachmentPrefix) + ".original"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment original unavailable or unsafe")
	}
	return root.Open(name)
}

func (s *AttachmentStore) ProtectPath(path string) bool {
	return s != nil && protectStoredPath(path, s.directory)
}

func attachmentText(data []byte) ([]byte, bool) {
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		decoded, err := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(data)
		if err != nil {
			return nil, false
		}
		data = decoded
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, false
	}
	if kind, _ := filetype.Match(data); kind != filetype.Unknown {
		return nil, false
	}
	for _, b := range data {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' || b == 0x7f {
			return nil, false
		}
	}
	return data, true
}
