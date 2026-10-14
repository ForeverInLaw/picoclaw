package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	manager "github.com/sipeed/picoclaw/pkg/mcp"
)

func TestMCPArtifactRuneThresholdAndResultVisibilityContract(t *testing.T) {
	var text string
	var isError bool
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "text-fixture", Version: "1"}, nil)
	definition := &sdkmcp.Tool{Name: "text", InputSchema: map[string]any{"type": "object"}}
	server.AddTool(definition, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}, IsError: isError}, nil
	})
	httpServer := httptest.NewServer(
		sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
			&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}),
	)
	defer httpServer.Close()
	m := manager.NewManager()
	defer m.Close()
	require.NoError(
		t,
		m.ConnectServer(t.Context(), "fixture", config.MCPServerConfig{Type: "http", URL: httpServer.URL}),
	)
	store := NewArtifactStore(
		config.MCPArtifactConfig{
			Directory:      filepath.Join(t.TempDir(), "artifacts"),
			MaxInlineRunes: 4,
			PreviewRunes:   2,
		},
		nil,
	)
	tool := NewMCPTool(m, "fixture", definition).WithArtifacts(store)
	scope := RequestScope{AgentID: "main", SessionKey: "agent:main:telegram:direct:42"}
	ctx := WithRequestScope(t.Context(), scope)
	for _, tc := range []struct {
		name, text         string
		errorResult, saved bool
	}{
		{"exact Unicode threshold", "\u754c\u754c\u754c\u754c", false, false},
		{"large Unicode result", "\u754c\u754c\u754c\u754c\u754c", false, true},
		{"large server error", "\u754c\u754c\u754c\u754c\u754c", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isError = tc.text, tc.errorResult
			result := tool.Execute(ctx, nil)
			require.Equal(t, tc.errorResult, result.IsError)
			require.False(t, result.Terminal)
			require.Empty(t, result.ForUser)
			require.Empty(t, result.ForActivity, "MCP private model output must not become public activity")
			if !tc.saved {
				require.Equal(t, text, result.ForLLM)
				return
			}
			ref := regexp.MustCompile(`artifact://mcp/[a-f0-9]+`).FindString(result.ForLLM)
			require.NotEmpty(t, ref)
			read := NewReadFileTool(t.TempDir(), true, 4).WithArtifacts(store)
			first := read.Execute(ctx, map[string]any{"path": ref})
			require.False(t, first.IsError)
			require.Contains(t, first.ForLLM, "offset=4")
			for _, rejected := range []RequestScope{
				{},
				{AgentID: "other", SessionKey: scope.SessionKey},
				{AgentID: scope.AgentID, SessionKey: "foreign"},
				{AgentID: scope.AgentID, SessionKey: scope.SessionKey, Inline: true},
				{AgentID: scope.AgentID, SessionKey: scope.SessionKey, NoHistory: true},
			} {
				denied := read.Execute(WithRequestScope(t.Context(), rejected), map[string]any{"path": ref})
				require.True(t, denied.IsError)
				require.NotContains(t, denied.ForLLM, "\u754c")
			}
		})
	}
	public := SilentResult("\u754c\u754c\u754c\u754c\u754c").WithActivity("explicit public detail").WithTerminal()
	public.ForUser = "already delivered public outcome"
	captured := store.Capture(scope, public, public.ForLLM)
	require.True(t, captured.Silent)
	require.True(t, captured.Terminal)
	require.Equal(t, "explicit public detail", captured.ForActivity)
	require.Equal(t, "already delivered public outcome", captured.ForUser)
}
