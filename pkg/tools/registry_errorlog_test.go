package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/toolerrors"
)

func TestToolRegistry_Execute_RecordsErrorToToolErrorLog(t *testing.T) {
	r := NewToolRegistry()
	log := toolerrors.New(filepath.Join(t.TempDir(), "logs", "tool_call_errors.jsonl"), 100)
	r.SetErrorLog(log)
	r.Register(&mockRegistryTool{
		name:   "boom",
		desc:   "fails",
		params: map[string]any{"type": "object"},
		result: ErrorResult("boom failed"),
	})

	result := r.ExecuteWithContext(context.Background(), "boom", map[string]any{"x": 1}, "telegram", "42", nil)
	if !result.IsError {
		t.Fatal("expected error result")
	}

	content, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := splitNonEmptyLines(string(content))
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	var entry toolerrors.Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if entry.Tool != "boom" {
		t.Fatalf("entry.Tool = %q, want boom", entry.Tool)
	}
	if entry.Stage != "execute" {
		t.Fatalf("entry.Stage = %q, want execute", entry.Stage)
	}
	if entry.Channel != "telegram" || entry.ChatID != "42" {
		t.Fatalf("unexpected target info: %#v", entry)
	}
}

func splitNonEmptyLines(value string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(value); i++ {
		if i < len(value) && value[i] != '\n' {
			continue
		}
		if i > start {
			out = append(out, value[start:i])
		}
		start = i + 1
	}
	return out
}
