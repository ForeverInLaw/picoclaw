package mcp

import (
	"fmt"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
)

func invalidFlags() error { return fmt.Errorf("invalid MCP flags; see picoclaw mcp help") }

func loadConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		return nil, fmt.Errorf("failed to load config; check JSON, credentials and environment settings")
	}
	return cfg, nil
}

func saveConfig(cfg *config.Config) error {
	if err := cfg.Tools.MCP.Validate(); err != nil {
		return err
	}
	if err := config.SaveConfig(internal.GetConfigPath(), cfg); err != nil {
		return fmt.Errorf("failed to save config; check file permissions and security settings")
	}
	return nil
}
