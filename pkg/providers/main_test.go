package providers

import (
	"fmt"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestMain points the Picoclaw home at a temporary directory, so answers served
// through Key pools in tests never record spend in the real home.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "picoclaw-providers-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv(config.EnvHome, home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
