package mcp

import "github.com/spf13/cobra"

func NewMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "mcp", Short: "Manage MCP server configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.SetFlagErrorFunc(func(*cobra.Command, error) error { return invalidFlags() })
	cmd.AddCommand(newAddCommand(false), newAddCommand(true), newRemoveCommand())
	cmd.AddCommand(newListCommand(), newProbeCommand("show"), newProbeCommand("test"))
	return cmd
}
