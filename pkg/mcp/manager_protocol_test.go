package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCallToolSerializesArgumentsAsObject(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "strict-fixture", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "record", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "accepted"}}}, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	requests := make(chan json.RawMessage, 3)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var request struct {
				Method string `json:"method"`
				Params struct {
					Arguments json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Method == "tools/call" {
				arguments := bytes.TrimSpace(request.Params.Arguments)
				requests <- arguments
				if len(arguments) == 0 || arguments[0] != '{' {
					http.Error(w, "arguments must be a JSON object", http.StatusBadRequest)
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	manager := NewManager()
	defer func() { require.NoError(t, manager.Close()) }()
	require.NoError(
		t,
		manager.ConnectServer(t.Context(), "strict", config.MCPServerConfig{Type: "http", URL: httpServer.URL}),
	)

	cases := []struct {
		name      string
		arguments map[string]any
		wantJSON  string
	}{
		{name: "nil arguments", wantJSON: `{}`},
		{name: "empty arguments", arguments: map[string]any{}, wantJSON: `{}`},
		{
			name: "nested arguments",
			arguments: map[string]any{
				"filters":         map[string]any{"states": []string{"Todo", "In Progress"}, "limit": 2},
				"includeArchived": false,
				"cursor":          nil,
			},
			wantJSON: `{"filters":{"states":["Todo","In Progress"],"limit":2},"includeArchived":false,"cursor":null}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.CallTool(t.Context(), "strict", "record", tc.arguments)
			require.NoError(t, err, "arguments must pass strict MCP request validation")
			require.False(t, result.IsError)
			require.Equal(t, "accepted", result.Content[0].(*sdkmcp.TextContent).Text)
			require.JSONEq(t, tc.wantJSON, string(<-requests))
			require.Empty(t, requests, "each call must send exactly one tools/call request")
		})
	}
}
