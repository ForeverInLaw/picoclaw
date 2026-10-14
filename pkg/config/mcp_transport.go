package config

import "strings"

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
