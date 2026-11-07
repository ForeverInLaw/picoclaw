package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/keyspend"
)

// TestMain points Key pool spend at a temporary file, so answers served through
// Key pools in tests never record spend in the real Picoclaw home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "picoclaw-providers-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	keyPools.spend = keyspend.NewStore(filepath.Join(dir, "spend.json"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
