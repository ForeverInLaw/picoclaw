package mcp

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use: "remove <name>", Short: "Remove an MCP server", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Tools.MCP.Servers[args[0]]; !ok {
				return fmt.Errorf("MCP server %q not found", args[0])
			}
			delete(cfg.Tools.MCP.Servers, args[0])
			if len(cfg.Tools.MCP.Servers) == 0 {
				cfg.Tools.MCP.Enabled = false
			}
			if err = saveConfig(cfg); err != nil {
				return err
			}
			cmd.Printf("MCP server %q removed.\n", args[0])
			return nil
		},
	}
}
