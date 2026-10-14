package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestIndependentMCPServersInitializeConcurrently(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "concurrent-fixture", Version: "1"}, nil)
	protocol := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read failed", http.StatusBadRequest)
				return
			}
			var request struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &request) == nil && request.Method == "initialize" {
				started <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		protocol.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	manager := NewManager(ManagerOptions{QuietDiagnostics: true})
	defer manager.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.LoadFromMCPConfig(ctx, config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				"first":  {Enabled: true, Type: "http", URL: httpServer.URL},
				"second": {Enabled: true, Type: "http", URL: httpServer.URL},
			},
		}, "")
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			once.Do(func() { close(release) })
			<-done
			t.Fatal("independent handshakes were serialized")
		}
	}
	once.Do(func() { close(release) })
	require.NoError(t, <-done)
	require.Len(t, manager.GetServers(), 2)
}

func TestMCPCloseWaitsForOperationAdmission(t *testing.T) {
	manager := NewManager(ManagerOptions{QuietDiagnostics: true})
	// Pause a CallTool admission after its closed check and before wg.Add.
	manager.mu.RLock()
	closed := make(chan struct{})
	go func() { _ = manager.Close(); close(closed) }()
	deadline := time.After(50 * time.Millisecond)
	for !manager.closed.Load() {
		select {
		case <-deadline:
			goto admitted
		default:
			time.Sleep(time.Millisecond)
		}
	}
admitted:
	manager.wg.Add(1)
	manager.mu.RUnlock()
	defer func() {
		manager.wg.Done()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("Close did not drain the admitted operation")
		}
	}()
	select {
	case <-closed:
		t.Fatal("Close passed Wait before an admitted operation registered")
	case <-time.After(50 * time.Millisecond):
	}
}
