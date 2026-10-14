package mcp

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newAddCommand() *cobra.Command {
	var transport, envFile string
	var env, headers []string
	var deferred, noDeferred, force bool
	cmd := &cobra.Command{
		Use: "add [flags] <name> <command-or-url> [args...]", Short: "Add or update an MCP server",
		DisableFlagParsing: true,
	}
	flags := cmd.Flags()
	flags.StringVarP(&transport, "transport", "t", "stdio", "Transport: stdio, http / streamable-http, or sse")
	flags.StringArrayVarP(&env, "env", "e", nil, "KEY=value environment variable (repeatable)")
	flags.StringVar(&envFile, "env-file", "", "Environment file for stdio, relative to workspace")
	flags.StringArrayVarP(&headers, "header", "H", nil, "Name: Value or Name=Value HTTP header (repeatable)")
	flags.BoolVar(&deferred, "deferred", false, "Hide tools until discovered")
	flags.BoolVar(&noDeferred, "no-deferred", false, "Keep tools always active")
	flags.BoolVarP(&force, "force", "f", false, "Update an existing server without prompting")
	cmd.RunE = func(cmd *cobra.Command, raw []string) error {
		args, err := parseAddArguments(flags, raw)
		if err != nil {
			return err
		}
		if help, _ := flags.GetBool("help"); help {
			return cmd.Help()
		}
		if len(args) < 2 {
			return fmt.Errorf("usage: picoclaw mcp add [flags] <name> [--] <command-or-url> [args...]")
		}
		if flags.Changed("deferred") && flags.Changed("no-deferred") {
			return fmt.Errorf("use only one of --deferred and --no-deferred")
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name := args[0]
		if _, exists := cfg.Tools.MCP.Servers[name]; exists && !force {
			return fmt.Errorf("MCP server %q already exists; use --force to update", name)
		}
		server := config.MCPServerConfig{Enabled: true, Type: config.NormalizeMCPTransportType(transport)}
		if flags.Changed("deferred") {
			server.Deferred = &deferred
		} else if flags.Changed("no-deferred") {
			active := !noDeferred
			server.Deferred = &active
		}
		switch server.Type {
		case "stdio":
			if len(headers) != 0 {
				return fmt.Errorf("--header requires http or sse transport")
			}
			if u, _ := url.Parse(args[1]); u != nil && (u.Scheme == "http" || u.Scheme == "https") {
				return fmt.Errorf("a remote URL requires --transport http or --transport sse")
			}
			server.Command, server.Args, server.EnvFile = args[1], args[2:], envFile
			server.Env, err = assignments(env, false)
		case "http", "sse":
			if len(env) != 0 || envFile != "" {
				return fmt.Errorf("--env and --env-file require stdio transport")
			}
			if len(args) != 2 {
				return fmt.Errorf("remote transport does not accept command arguments")
			}
			u, parseErr := url.ParseRequestURI(args[1])
			if parseErr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid MCP URL; expected http or https endpoint")
			}
			server.URL = args[1]
			server.Headers, err = assignments(headers, true)
		default:
			return fmt.Errorf("unsupported transport; use stdio, http, streamable-http or sse")
		}
		if err != nil {
			return err
		}
		if cfg.Tools.MCP.Servers == nil {
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
		}
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers[name] = server
		if err = saveConfig(cfg); err != nil {
			return err
		}
		cmd.Printf("MCP server %q saved.\n", name)
		return nil
	}
	return cmd
}

// pflag owns option parsing. The first unknown option after the executable starts
// a raw argv tail, matching pinned CLI invocation without a required separator.
func parseAddArguments(flags *pflag.FlagSet, args []string) ([]string, error) {
	positionals := 0
	end := len(args)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals++
			continue
		}
		name, _, assigned := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		flag := flags.Lookup(name)
		attached := false
		if !strings.HasPrefix(arg, "--") && len(arg) > 1 {
			flag = flags.ShorthandLookup(arg[1:2])
			attached = len(arg) > 2
		}
		if flag == nil {
			if positionals >= 2 {
				end = i
				break
			}
			return nil, invalidFlags()
		}
		if flag.NoOptDefVal == "" && !assigned && !attached {
			i++
		}
	}
	if err := flags.Parse(args[:end]); err != nil {
		return nil, invalidFlags()
	}
	return append(flags.Args(), args[end:]...), nil
}

func assignments(values []string, header bool) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, entry := range values {
		separator := "="
		if header && strings.Contains(entry, ":") {
			separator = ":"
		}
		key, value, ok := strings.Cut(entry, separator)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid assignment; use KEY=value for env or Name: Value for headers")
		}
		if header {
			value = strings.TrimSpace(value)
		}
		result[key] = value
	}
	return result, nil
}
