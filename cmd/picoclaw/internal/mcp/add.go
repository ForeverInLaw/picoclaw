package mcp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newAddCommand(update bool) *cobra.Command {
	var transport, envFile string
	var env, headers []string
	var deferred, noDeferred, force, enabled bool
	cmd := &cobra.Command{
		Use: "add [flags] <name> <command-or-url> [args...]", Short: "Add or update an MCP server",
		DisableFlagParsing: true,
	}
	if update {
		cmd.Use = "update [flags] <name> <command-or-url> [args...]"
		cmd.Short = "Update an existing MCP server, preserving unspecified options"
	}
	flags := cmd.Flags()
	flags.StringVarP(&transport, "transport", "t", "stdio", "Transport: stdio, http / streamable-http, or sse")
	flags.StringArrayVarP(&env, "env", "e", nil, "KEY=value environment variable (repeatable)")
	flags.StringVar(&envFile, "env-file", "", "Environment file for stdio, relative to workspace")
	flags.StringArrayVarP(&headers, "header", "H", nil, "Name: Value or Name=Value HTTP header (repeatable)")
	flags.BoolVar(&deferred, "deferred", false, "Hide tools until discovered")
	flags.BoolVar(&noDeferred, "no-deferred", false, "Keep tools always active")
	flags.BoolVarP(&force, "force", "f", false, "Update an existing server without prompting")
	flags.BoolVar(&enabled, "enabled", true, "Enable or disable this server (--enabled=false)")
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
		server, exists := cfg.Tools.MCP.Servers[name]
		if update && !exists {
			return fmt.Errorf("MCP server %q not found", name)
		}
		if exists && !force && !update {
			cmd.Printf("MCP server %q already exists. Update? [y/N]: ", name)
			var answer string
			if _, scanErr := fmt.Fscanln(cmd.InOrStdin(), &answer); scanErr != nil && scanErr != io.EOF {
				return fmt.Errorf("could not read update confirmation")
			}
			if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
				return fmt.Errorf("MCP server %q unchanged; use --force to update", name)
			}
		}
		previousTransport := config.EffectiveMCPTransportType(server)
		if exists && !flags.Changed("transport") && previousTransport != "" {
			transport = previousTransport
		}
		server.Type = config.NormalizeMCPTransportType(transport)
		if !exists || flags.Changed("enabled") {
			server.Enabled = enabled
		}
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
			server.Command, server.Args, server.URL = expandCommandPath(args[1]), args[2:], ""
			if localPath := strings.ContainsAny(server.Command, `/\`); localPath {
				info, statErr := os.Stat(server.Command)
				if statErr != nil || info.IsDir() {
					return fmt.Errorf("local command does not exist or is not a file")
				}
				if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
					return fmt.Errorf("local command is not executable")
				}
			}
			if flags.Changed("env-file") {
				server.EnvFile = envFile
			}
			values, parseErr := assignments(env, false)
			err = parseErr
			server.Env = mergeAssignments(server.Env, values)
			server.Headers = nil
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
			values, parseErr := assignments(headers, true)
			err = parseErr
			for key := range values {
				for previous := range server.Headers {
					if strings.EqualFold(key, previous) {
						delete(server.Headers, previous)
					}
				}
			}
			server.Headers = mergeAssignments(server.Headers, values)
			server.Command, server.Args, server.EnvFile, server.Env = "", nil, "", nil
		default:
			return fmt.Errorf("unsupported transport; use stdio, http, streamable-http or sse")
		}
		if err != nil {
			return err
		}
		if cfg.Tools.MCP.Servers == nil {
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
		}
		if (!exists || flags.Changed("enabled")) && server.Enabled {
			cfg.Tools.MCP.Enabled = true
		}
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
		colon, equals := strings.IndexByte(entry, ':'), strings.IndexByte(entry, '=')
		if header && colon >= 0 && (equals < 0 || colon < equals) {
			separator = ":"
		}
		key, value, ok := strings.Cut(entry, separator)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid assignment; use KEY=value for env or Name: Value for headers")
		}
		if header {
			key = http.CanonicalHeaderKey(key)
			value = strings.TrimSpace(value)
		}
		result[key] = value
	}
	return result, nil
}

func mergeAssignments(previous, changes map[string]string) map[string]string {
	if len(changes) == 0 {
		return previous
	}
	if previous == nil {
		previous = map[string]string{}
	}
	for key, value := range changes {
		previous[key] = value
	}
	return previous
}

func expandCommandPath(path string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
