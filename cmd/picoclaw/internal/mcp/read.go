package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

func newListCommand() *cobra.Command {
	var status bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "list", Short: "List configured MCP servers", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.Tools.MCP.Servers))
			for name := range cfg.Tools.MCP.Servers {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				cmd.Println("No MCP servers configured.")
			}
			for _, name := range names {
				server := cfg.Tools.MCP.Servers[name]
				state := "disabled"
				if server.Enabled {
					state = "enabled"
				}
				if status && server.Enabled {
					tools, probeErr := probe(cmd.Context(), cfg, name, timeout)
					if probeErr != nil {
						state = "error (connection or tools list failed)"
					} else {
						state = fmt.Sprintf("ok (%d tools)", len(tools))
					}
				}
				cmd.Printf(
					"%s\t%s\t%s\t%s\tdeferred=%t\n",
					name,
					config.EffectiveMCPTransportType(server),
					safeTarget(server),
					state,
					effectiveDeferred(server, cfg.Tools.MCP.Discovery.Enabled),
				)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "Probe enabled servers for live status")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Timeout per live status probe")
	return cmd
}

func newProbeCommand(kind string) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: kind + " <name>", Short: "Show configured server details and tools", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			server, ok := cfg.Tools.MCP.Servers[name]
			if !ok {
				return fmt.Errorf("MCP server %q not found", name)
			}
			redact := serverRedactor(server, cfg.WorkspacePath())
			if kind == "show" {
				cmd.Printf(
					"Server: %s\nTransport: %s\nTarget: %s\nEnabled: %t\nDeferred: %t (explicit: %t)\n",
					name,
					config.EffectiveMCPTransportType(server),
					safeTarget(server),
					server.Enabled,
					effectiveDeferred(server, cfg.Tools.MCP.Discovery.Enabled),
					server.Deferred != nil,
				)
				cmd.Printf(
					"Env file: %s\nEnv keys: %s\nHeader names: %s\n",
					redact.Replace(server.EnvFile),
					strings.Join(sortedKeys(server.Env), ", "),
					strings.Join(sortedKeys(server.Headers), ", "),
				)
				if !server.Enabled {
					return nil
				}
			}
			tools, err := probe(cmd.Context(), cfg, name, timeout)
			if err != nil {
				return err
			}
			cmd.Printf("MCP server %q reachable (%d tools).\n", name, len(tools))
			if kind == "show" {
				for _, tool := range tools {
					schema, _ := json.MarshalIndent(tool.InputSchema, "", "  ")
					cmd.Print(
						redact.Replace(
							fmt.Sprintf("Tool: %s\n%s\nParameters: %s\n", tool.Name, tool.Description, schema),
						),
					)
				}
			}
			return nil
		},
	}
	if kind == "test" {
		cmd.Short = "Test MCP initialize and tools list without invoking tools"
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Connection and tools list timeout")
	return cmd
}

func probe(ctx context.Context, cfg *config.Config, name string, timeout time.Duration) ([]*sdkmcp.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	manager := picomcp.NewManager(
		picomcp.ManagerOptions{QuietDiagnostics: true, RequireToolsList: true, HTTPTimeout: timeout},
	)
	defer manager.Close()
	server := cfg.Tools.MCP.Servers[name]
	server.Enabled = true
	selected := cfg.Tools.MCP
	selected.Enabled = true
	selected.Servers = map[string]config.MCPServerConfig{name: server}
	if err := manager.LoadFromMCPConfig(ctx, selected, cfg.WorkspacePath(), cfg.Isolation); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("MCP server %q probe canceled or timed out: %w", name, ctx.Err())
		}
		return nil, fmt.Errorf(
			"MCP server %q connection or tools list failed; check endpoint, transport, headers, env_file and isolation settings",
			name,
		)
	}
	connection, ok := manager.GetServer(name)
	if !ok {
		return nil, fmt.Errorf("MCP server %q did not register a connection", name)
	}
	return connection.Tools, nil
}

func effectiveDeferred(server config.MCPServerConfig, inherited bool) bool {
	if server.Deferred != nil {
		return *server.Deferred
	}
	return inherited
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func safeTarget(server config.MCPServerConfig) string {
	if config.EffectiveMCPTransportType(server) == "stdio" {
		return serverRedactor(server).Replace(fmt.Sprintf("%s (%d args)", server.Command, len(server.Args)))
	}
	u, err := url.Parse(server.URL)
	if err != nil {
		return "<invalid URL>"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return serverRedactor(server).Replace(u.String())
}

func serverRedactor(server config.MCPServerConfig, workspace ...string) *strings.Replacer {
	var values []string
	env := server.Env
	if config.EffectiveMCPTransportType(server) == "stdio" && server.EnvFile != "" && len(workspace) > 0 {
		if !filepath.IsAbs(server.EnvFile) {
			server.EnvFile = filepath.Join(workspace[0], server.EnvFile)
		}
		if declared, err := picomcp.ReadDeclaredEnvironment(server); err == nil {
			env = declared
		}
	}
	for _, value := range env {
		values = append(values, value)
	}
	for _, value := range server.Headers {
		values = append(values, value)
		if _, credential, ok := strings.Cut(value, " "); ok {
			values = append(values, credential)
		}
	}
	if u, err := url.Parse(server.URL); err == nil {
		if u.User != nil {
			password, _ := u.User.Password()
			values = append(values, u.User.Username(), password)
		}
		for _, entries := range u.Query() {
			values = append(values, entries...)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, len(values)*2)
	for _, value := range values {
		if value != "" {
			pairs = append(pairs, value, "[REDACTED]")
		}
	}
	return strings.NewReplacer(pairs...)
}
