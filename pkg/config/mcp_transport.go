package config

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeMCPTransportType accepts the pinned streamable HTTP transport aliases.
func NormalizeMCPTransportType(transport string) string {
	name := strings.ToLower(strings.TrimSpace(transport))
	if name == "streamable-http" || name == "streamable_http" || name == "streamablehttp" {
		return "http"
	}
	return name
}

func EffectiveMCPTransportType(server MCPServerConfig) string {
	if name := NormalizeMCPTransportType(server.Type); name != "" {
		return name
	}
	if server.URL != "" {
		return "sse"
	}
	if server.Command != "" {
		return "stdio"
	}
	return ""
}

// Validate checks enabled server targets without echoing credential values.
// Serialized field/type validation remains owned by the existing Config loader.
func (c MCPConfig) Validate() error {
	for name, server := range c.Servers {
		if !server.Enabled {
			continue
		}
		switch EffectiveMCPTransportType(server) {
		case "stdio":
			if strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("tools.mcp.servers.%s.command is required for stdio", name)
			}
		case "http", "sse":
			u, err := url.ParseRequestURI(server.URL)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("tools.mcp.servers.%s.url must be an http or https endpoint", name)
			}
		default:
			return fmt.Errorf("tools.mcp.servers.%s.type must be stdio, http, streamable-http or sse", name)
		}
	}
	return nil
}
