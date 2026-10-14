package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextManagerSelectionRejectsInvalidEngineAndBounds(t *testing.T) {
	for _, tc := range []struct{ name, input, path string }{
		{"unknown engine", `{"agents":{"defaults":{"context_manager":"typo"}}}`, "agents.defaults.context_manager"},
		{"negative concurrency", `{"agents":{"defaults":{"context_manager":"seahorse","context_manager_config":{"max_concurrent_compactions":-1}}}}`, "max_concurrent_compactions"},
		{"agent override", `{"agents":{"list":[{"id":"reviewer","context_manager":"typo"}]}}`, "agents.list[0].context_manager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("expected actionable %s error, got %v", tc.path, err)
			}
		})
	}
}
