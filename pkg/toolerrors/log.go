package toolerrors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const DefaultLimit = 100

type Entry struct {
	Timestamp  time.Time      `json:"timestamp"`
	Tool       string         `json:"tool"`
	Stage      string         `json:"stage"`
	Channel    string         `json:"channel,omitempty"`
	ChatID     string         `json:"chat_id,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      string         `json:"error"`
	Args       map[string]any `json:"args,omitempty"`
}

type Log struct {
	path  string
	limit int
	mu    sync.Mutex
}

func New(path string, limit int) *Log {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Log{path: strings.TrimSpace(path), limit: limit}
}

func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Log) Record(entry Entry) error {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	line, err := marshalEntry(entry)
	if err != nil {
		return err
	}

	existing, err := os.ReadFile(l.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("toolerrors: read log: %w", err)
	}

	lines := compactLines(existing)
	if len(lines) >= l.limit {
		lines = lines[len(lines)-l.limit+1:]
	}
	lines = append(lines, line)

	var buf bytes.Buffer
	for _, item := range lines {
		buf.Write(item)
		buf.WriteByte('\n')
	}
	if err := fileutil.WriteFileAtomic(l.path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("toolerrors: write log: %w", err)
	}
	return nil
}

func marshalEntry(entry Entry) ([]byte, error) {
	data, err := json.Marshal(entry)
	if err == nil {
		return data, nil
	}
	fallback := struct {
		Timestamp  time.Time `json:"timestamp"`
		Tool       string    `json:"tool"`
		Stage      string    `json:"stage"`
		Channel    string    `json:"channel,omitempty"`
		ChatID     string    `json:"chat_id,omitempty"`
		DurationMS int64     `json:"duration_ms,omitempty"`
		Error      string    `json:"error"`
		Args       string    `json:"args,omitempty"`
	}{
		Timestamp:  entry.Timestamp,
		Tool:       entry.Tool,
		Stage:      entry.Stage,
		Channel:    entry.Channel,
		ChatID:     entry.ChatID,
		DurationMS: entry.DurationMS,
		Error:      entry.Error,
		Args:       fmt.Sprintf("%v", entry.Args),
	}
	data, marshalErr := json.Marshal(fallback)
	if marshalErr != nil {
		return nil, fmt.Errorf("toolerrors: marshal entry: %w", marshalErr)
	}
	return data, nil
}

func compactLines(content []byte) [][]byte {
	if len(content) == 0 {
		return nil
	}
	raw := bytes.Split(content, []byte("\n"))
	lines := make([][]byte, 0, len(raw))
	for _, line := range raw {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		copied := make([]byte, len(line))
		copy(copied, line)
		lines = append(lines, copied)
	}
	return lines
}
