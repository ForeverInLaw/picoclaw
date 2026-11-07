package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestErrorTextNeverRevealsAKeyPoolSecret(t *testing.T) {
	cfg := &config.Config{KeyPools: []config.KeyPool{{ID: "orgs", Keys: []config.KeyPoolKey{
		{ID: "first", Label: "First org", Enabled: true, APIKey: "sk-ant-api03-pool-secret"},
	}}}}

	text := safeErrorText(cfg, errors.New("request with key sk-ant-api03-pool-secret was rejected"))

	if strings.Contains(text, "sk-ant-api03-pool-secret") {
		t.Fatalf("error text = %q, want the pool secret redacted", text)
	}
}
