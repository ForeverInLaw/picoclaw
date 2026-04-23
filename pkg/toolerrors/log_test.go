package toolerrors

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLog_RecordCapsAtLimit(t *testing.T) {
	log := New(filepath.Join(t.TempDir(), "logs", "tool_call_errors.jsonl"), 3)
	for i := 1; i <= 5; i++ {
		if err := log.Record(Entry{Tool: "exec", Stage: "execute", Error: string(rune('0' + i))}); err != nil {
			t.Fatalf("Record(%d) error = %v", i, err)
		}
	}

	content, err := readLogFile(log.Path())
	if err != nil {
		t.Fatalf("readLogFile() error = %v", err)
	}
	if len(content) != 3 {
		t.Fatalf("len(content) = %d, want 3", len(content))
	}
	if content[0].Error != "3" || content[1].Error != "4" || content[2].Error != "5" {
		t.Fatalf("unexpected entries: %#v", content)
	}
}

func TestLog_RecordFallsBackWhenArgsAreNotJSONMarshalable(t *testing.T) {
	log := New(filepath.Join(t.TempDir(), "logs", "tool_call_errors.jsonl"), 10)
	badArgs := map[string]any{"fn": func() {}}
	if err := log.Record(Entry{Tool: "exec", Stage: "execute", Error: "boom", Args: badArgs}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	data, err := readRawLines(log.Path())
	if err != nil {
		t.Fatalf("readRawLines() error = %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(data))
	}
	var parsed map[string]any
	if err := json.Unmarshal(data[0], &parsed); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if parsed["args"] == nil {
		t.Fatalf("expected fallback args string, got %#v", parsed)
	}
}
