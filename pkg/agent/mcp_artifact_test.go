package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestMCPArtifactPreviewAndReadRemainScopedAcrossReopenAndClear(t *testing.T) {
	const secret = "fixture-complete-secret-key"
	raw := strings.Repeat("界", 110) + " " + secret + strings.Repeat(" durable detail", 2500) + " END-OF-ARTIFACT"
	var executions atomic.Int32
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "artifact-fixture", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "large", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			executions.Add(1)
			require.JSONEq(t, `{}`, string(request.Params.Arguments))
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: raw}}}, nil
		})
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	defer httpServer.Close()
	base := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(base, "workspace")
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.ContextWindow = 128000
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Tools.FilterSensitiveData = false
	cfg.ModelList = []config.ModelConfig{{ModelName: "test-model", Model: "openai/test-model", APIKey: secret}}
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{"fixture": {Enabled: true, Type: "http", URL: httpServer.URL}}
	path := filepath.Join(base, "config.json")
	require.NoError(t, config.SaveConfig(path, cfg))
	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(encoded, &data))
	data["tools"].(map[string]any)["mcp"].(map[string]any)["artifacts"] = map[string]any{
		"directory": filepath.Join(base, "artifacts"), "max_inline_runes": 256, "preview_runes": 128,
	}
	encoded, err = json.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	cfg, err = config.LoadConfig(path)
	require.NoError(t, err)
	p := &sourceScenarioProvider{tool: "mcp_fixture_large", args: map[string]any{}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer func() { al.Close() }()
	message := bus.InboundMessage{
		Channel: "telegram", ChatID: "42", SenderID: "42", Peer: bus.Peer{Kind: "direct", ID: "42"},
		Content: "query sources", SessionKey: "agent:main:telegram:direct:42",
	}
	process := func() (string, error) {
		return al.ProcessDirectWithChannel(t.Context(), message.Content, message.SessionKey, message.Channel, message.ChatID)
	}
	_, err = process()
	require.NoError(t, err)
	preview := p.expandOutput
	require.Less(t, utf8.RuneCountInString(preview), 600)
	require.NotContains(t, preview, "fixture-complete")
	require.Contains(t, preview, "[FILTERED]")
	ref := regexp.MustCompile(`artifact://mcp/[a-f0-9]+`).FindString(preview)
	require.NotEmpty(t, ref)
	require.Equal(t, int32(1), executions.Load())
	al.Close()
	al = NewAgentLoop(cfg, bus.NewMessageBus(), p)
	message.Content = "/clear"
	_, err = process()
	require.NoError(t, err)
	p.tool, p.args = "read_file", map[string]any{"path": ref}
	message.Content = "query sources"
	_, err = process()
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "END-OF-ARTIFACT")
	require.Contains(t, p.expandOutput, "[FILTERED]")
	require.NotContains(t, p.expandOutput, secret)
	require.Equal(t, int32(1), executions.Load(), "explicit read must not replay MCP")
}
