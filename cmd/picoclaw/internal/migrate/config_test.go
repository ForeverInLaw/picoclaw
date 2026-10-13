package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestPrepareConfigForReadOnlyMountPreservesSource(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "old.json"), filepath.Join(dir, "v3.json")
	legacy := `{"channels":{"telegram":{"token":"secret","enabled":true}}}`
	if err := os.WriteFile(source, []byte(legacy), 0o400); err != nil {
		t.Fatal(err)
	}
	securityPath := filepath.Join(dir, config.SecurityConfigFile)
	security := "channels:\n  telegram:\n    token: sidecar-secret\n"
	if err := os.WriteFile(securityPath, []byte(security), 0o400); err != nil {
		t.Fatal(err)
	}
	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{"--config", source, "--output", output})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	bytes, _ := os.ReadFile(source)
	if string(bytes) != legacy {
		t.Fatal("migration modified source")
	}
	bytes, _ = os.ReadFile(securityPath)
	if string(bytes) != security {
		t.Fatal("preparation modified the source sidecar")
	}
	cfg, err := config.LoadConfig(output)
	if err != nil || cfg.Version != 3 || cfg.Channels.Telegram.Token != "sidecar-secret" {
		t.Fatalf("prepared config is unusable: %v", err)
	}
}
