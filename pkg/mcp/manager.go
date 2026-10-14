package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// headerTransport is an http.RoundTripper that adds custom headers to requests
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add custom headers
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}

	// Use the base transport
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// loadEnvFile loads environment variables from a file in .env format
// Each line should be in the format: KEY=value
// Lines starting with # are comments
// Empty lines are ignored
func loadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	envVars := make(map[string]string)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("invalid format at line %d: empty key", lineNum)
		}

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		envVars[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading env file: %w", err)
	}

	return envVars, nil
}

// ServerConnection represents a connection to an MCP server
type ServerConnection struct {
	Name    string
	Client  *mcp.Client
	Session *mcp.ClientSession
	Tools   []*mcp.Tool

	command *exec.Cmd
	config  config.MCPServerConfig
	cancel  context.CancelFunc
	stopped atomic.Bool
}

func (c *ServerConnection) stopProcess() {
	if c.command != nil && !c.stopped.Swap(true) {
		c.cancel()
		_ = isolation.Terminate(c.command)
	}
}

// Manager manages multiple MCP server connections
// ManagerOptions controls operator probes without changing normal runtime logging.
type ManagerOptions struct {
	QuietDiagnostics bool
	RequireToolsList bool
}

type Manager struct {
	options     ManagerOptions
	servers     map[string]*ServerConnection
	mu          sync.RWMutex
	closed      atomic.Bool    // changed from bool to atomic.Bool to avoid TOCTOU race
	wg          sync.WaitGroup // tracks in-flight CallTool calls
	lifecycleMu sync.Mutex     // serializes connect/reconnect with final shutdown
	lifetime    context.Context
	cancel      context.CancelFunc
	launch      isolation.Options
}

// NewManager creates a new MCP manager
func NewManager(options ...ManagerOptions) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	var selected ManagerOptions
	if len(options) > 0 {
		selected = options[0]
	}
	return &Manager{
		options:  selected,
		servers:  make(map[string]*ServerConnection),
		lifetime: ctx,
		cancel:   cancel,
	}
}

// LoadFromConfig loads MCP servers from configuration
func (m *Manager) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	return m.LoadFromMCPConfig(ctx, cfg.Tools.MCP, cfg.WorkspacePath(), cfg.Isolation)
}

// LoadFromMCPConfig loads MCP servers from MCP configuration and workspace path.
// This is the minimal dependency version that doesn't require the full Config object.
func (m *Manager) LoadFromMCPConfig(
	ctx context.Context,
	mcpCfg config.MCPConfig,
	workspacePath string,
	policies ...config.IsolationConfig,
) error {
	m.mu.Lock()
	m.launch.Workspace = workspacePath
	if len(policies) > 0 {
		m.launch.Policy = policies[0]
	}
	m.mu.Unlock()
	if !mcpCfg.Enabled {
		m.log(logger.InfoCF, "mcp", "MCP integration is disabled", nil)
		return nil
	}

	if len(mcpCfg.Servers) == 0 {
		m.log(logger.InfoCF, "mcp", "No MCP servers configured", nil)
		return nil
	}

	m.log(logger.InfoCF, "mcp", "Initializing MCP servers",
		map[string]any{
			"count": len(mcpCfg.Servers),
		})

	var wg sync.WaitGroup
	errs := make(chan error, len(mcpCfg.Servers))
	enabledCount := 0

	for name, serverCfg := range mcpCfg.Servers {
		if !serverCfg.Enabled {
			m.log(logger.DebugCF, "mcp", "Skipping disabled server",
				map[string]any{
					"server": name,
				})
			continue
		}

		enabledCount++
		wg.Add(1)
		go func(name string, serverCfg config.MCPServerConfig, workspace string) {
			defer wg.Done()

			// Resolve relative envFile paths relative to workspace
			if serverCfg.EnvFile != "" && !filepath.IsAbs(serverCfg.EnvFile) {
				if workspace == "" {
					err := fmt.Errorf(
						"workspace path is empty while resolving relative envFile %q for server %s",
						serverCfg.EnvFile,
						name,
					)
					m.log(logger.ErrorCF, "mcp", "Invalid MCP server configuration",
						map[string]any{
							"server":   name,
							"env_file": serverCfg.EnvFile,
							"error":    err.Error(),
						})
					errs <- err
					return
				}
				serverCfg.EnvFile = filepath.Join(workspace, serverCfg.EnvFile)
			}

			if err := m.ConnectServer(ctx, name, serverCfg); err != nil {
				m.log(logger.ErrorCF, "mcp", "Failed to connect to MCP server",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				errs <- fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		}(name, serverCfg, workspacePath)
	}

	wg.Wait()
	close(errs)

	// Collect errors
	var allErrors []error
	for err := range errs {
		allErrors = append(allErrors, err)
	}

	connectedCount := len(m.GetServers())

	// If all enabled servers failed to connect, return aggregated error
	if enabledCount > 0 && connectedCount == 0 {
		m.log(logger.ErrorCF, "mcp", "All MCP servers failed to connect",
			map[string]any{
				"failed": len(allErrors),
				"total":  enabledCount,
			})
		return errors.Join(allErrors...)
	}

	if len(allErrors) > 0 {
		m.log(logger.WarnCF, "mcp", "Some MCP servers failed to connect",
			map[string]any{
				"failed":    len(allErrors),
				"connected": connectedCount,
				"total":     enabledCount,
			})
		// Don't fail completely if some servers successfully connected
	}

	m.log(logger.InfoCF, "mcp", "MCP server initialization complete",
		map[string]any{
			"connected": connectedCount,
			"total":     enabledCount,
		})

	return nil
}

// ConnectServer connects to a single MCP server
func (m *Manager) ConnectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.connectServer(ctx, name, cfg)
}

func (m *Manager) connectServer(ctx context.Context, name string, cfg config.MCPServerConfig) error {
	if m.closed.Load() {
		return fmt.Errorf("manager is closed")
	}
	// Startup uses the caller's deadline; the established process belongs to the
	// manager, so completion of the initializing request cannot kill future calls.
	startupCtx, cancelStartup := context.WithCancel(ctx)
	stopStartup := context.AfterFunc(m.lifetime, cancelStartup)
	defer func() { stopStartup(); cancelStartup() }()
	ctx = startupCtx
	m.log(logger.InfoCF, "mcp", "Connecting to MCP server",
		map[string]any{
			"server":     name,
			"command":    cfg.Command,
			"args_count": len(cfg.Args),
		})

	// Create client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "picoclaw",
		Version: "1.0.0",
	}, nil)

	// Create transport based on configuration
	// Auto-detect transport type if not explicitly specified
	var transport mcp.Transport
	var command *exec.Cmd
	var cancelProcess context.CancelFunc
	var stopStartupProcess func() bool
	connected := false
	defer func() {
		if !connected && command != nil {
			cancelProcess()
			_ = isolation.Terminate(command)
		}
	}()
	transportType := config.EffectiveMCPTransportType(cfg)

	switch transportType {
	case "sse", "http":
		if cfg.URL == "" {
			return fmt.Errorf("URL is required for SSE/HTTP transport")
		}
		m.log(logger.DebugCF, "mcp", "Using SSE/HTTP transport",
			map[string]any{
				"server": name,
				"url":    cfg.URL,
			})

		sseTransport := &mcp.StreamableClientTransport{
			Endpoint: cfg.URL,
		}

		// Add custom headers if provided
		if len(cfg.Headers) > 0 {
			// Create a custom HTTP client with header-injecting transport
			sseTransport.HTTPClient = &http.Client{
				Transport: &headerTransport{
					base:    http.DefaultTransport,
					headers: cfg.Headers,
				},
			}
			m.log(logger.DebugCF, "mcp", "Added custom HTTP headers",
				map[string]any{
					"server":       name,
					"header_count": len(cfg.Headers),
				})
		}

		transport = sseTransport
	case "stdio":
		if cfg.Command == "" {
			return fmt.Errorf("command is required for stdio transport")
		}
		m.log(logger.DebugCF, "mcp", "Using stdio transport",
			map[string]any{
				"server":  name,
				"command": cfg.Command,
			})
		envMap, envErr := ReadDeclaredEnvironment(cfg)
		if envErr != nil {
			return fmt.Errorf("failed to load declared environment: %w", envErr)
		}
		m.log(logger.DebugCF, "mcp", "Loaded declared environment", map[string]any{"server": name, "var_count": len(envMap)})

		m.mu.RLock()
		options := m.launch
		m.mu.RUnlock()
		options.DeclaredEnv = envMap
		if options.Policy.Enabled {
			options.Dir = options.Workspace
		}
		processCtx, cancel := context.WithCancel(m.lifetime)
		cancelProcess = cancel
		// Startup cancellation also covers the actual namespace capability probe.
		stopStartupProcess = context.AfterFunc(ctx, cancelProcess)
		defer stopStartupProcess()
		var err error
		command, err = isolation.Command(processCtx, options, cfg.Command, cfg.Args...)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		transport = &mcp.CommandTransport{Command: command}
	default:
		return fmt.Errorf(
			"unsupported transport type: %s (supported: stdio, sse, http)",
			transportType,
		)
	}

	// Connect to server
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if command != nil {
			return fmt.Errorf(
				"local MCP startup failed; check command, isolation runtime and explicit exposed paths: %w",
				err,
			)
		}
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Get server info
	initResult := session.InitializeResult()
	m.log(logger.InfoCF, "mcp", "Connected to MCP server",
		map[string]any{
			"server":        name,
			"serverName":    initResult.ServerInfo.Name,
			"serverVersion": initResult.ServerInfo.Version,
			"protocol":      initResult.ProtocolVersion,
		})

	// List available tools if supported
	var tools []*mcp.Tool
	if initResult.Capabilities.Tools != nil {
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				if m.options.RequireToolsList {
					if command != nil {
						cancelProcess()
						_ = isolation.Terminate(command)
					}
					_ = session.Close()
					return fmt.Errorf("failed to list MCP tools: %w", err)
				}
				m.log(logger.WarnCF, "mcp", "Error listing tool",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				continue
			}
			tools = append(tools, tool)
		}

		m.log(logger.InfoCF, "mcp", "Listed tools from MCP server",
			map[string]any{
				"server":    name,
				"toolCount": len(tools),
			})
	}

	// Store connection
	if stopStartupProcess != nil && !stopStartupProcess() {
		cancelProcess()
		_ = session.Close()
		return fmt.Errorf("MCP startup canceled: %w", ctx.Err())
	}
	m.mu.Lock()
	if m.closed.Load() || ctx.Err() != nil {
		m.mu.Unlock()
		if command != nil {
			cancelProcess()
			_ = isolation.Terminate(command)
		}
		_ = session.Close()
		reason := ctx.Err()
		if reason == nil {
			reason = context.Canceled
		}
		return fmt.Errorf("MCP startup canceled: %w", reason)
	}
	previous := m.servers[name]
	m.servers[name] = &ServerConnection{
		Name:    name,
		Client:  client,
		Session: session,
		Tools:   tools,
		command: command,
		config:  cfg,
		cancel:  cancelProcess,
	}
	m.mu.Unlock()
	connected = true
	if previous != nil {
		previous.stopProcess()
		_ = previous.Session.Close()
	}

	return nil
}

// GetServers returns all connected servers
func (m *Manager) GetServers() map[string]*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerConnection, len(m.servers))
	for k, v := range m.servers {
		result[k] = v
	}
	return result
}

// GetServer returns a specific server connection
func (m *Manager) GetServer(name string) (*ServerConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.servers[name]
	return conn, ok
}

// CallTool calls a tool on a specific server
func (m *Manager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	// Check if closed before acquiring lock (fast path)
	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}

	m.mu.RLock()
	// Double-check after acquiring lock to prevent TOCTOU race
	if m.closed.Load() {
		m.mu.RUnlock()
		return nil, fmt.Errorf("manager is closed")
	}
	conn, ok := m.servers[serverName]
	if ok {
		m.wg.Add(1) // Add to WaitGroup while holding the lock
	}
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}
	defer m.wg.Done()
	if conn.stopped.Load() {
		var err error
		conn, err = m.reconnect(ctx, serverName)
		if err != nil {
			return nil, fmt.Errorf("failed to reconnect local MCP server: %w", err)
		}
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stopManager := context.AfterFunc(m.lifetime, cancel)
	defer func() { stopManager(); cancel() }()
	// A stdio session cannot identify or kill only one request's descendants.
	// Stop the owned session; a later independent call reconnects without replay.
	stopProcess := context.AfterFunc(requestCtx, conn.stopProcess)
	defer stopProcess()

	// A typed nil map would serialize as null; MCP tool arguments are an object.
	if arguments == nil {
		arguments = map[string]any{}
	}
	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}

	result, err := conn.Session.CallTool(requestCtx, params)
	if err != nil {
		requestErr := requestCtx.Err()
		if requestErr == nil {
			requestErr = m.lifetime.Err()
		}
		if requestErr != nil {
			conn.stopProcess()
			if conn.command != nil {
				return nil, fmt.Errorf(
					"MCP request canceled; local server stopped and session state may be lost: %w",
					requestErr,
				)
			}
			return nil, fmt.Errorf("MCP request canceled: %w", requestErr)
		}
		if conn.stopped.Load() {
			return nil, fmt.Errorf("local MCP session interrupted by cancellation; tool was not replayed: %w", err)
		}
		return nil, fmt.Errorf("failed to call tool: %w", err)
	}

	return result, nil
}

func (m *Manager) reconnect(ctx context.Context, name string) (*ServerConnection, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}
	m.mu.RLock()
	conn := m.servers[name]
	m.mu.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("server %s not found", name)
	}
	if conn.stopped.Load() {
		if err := m.connectServer(ctx, name, conn.config); err != nil {
			return nil, err
		}
		m.mu.RLock()
		conn = m.servers[name]
		m.mu.RUnlock()
	}
	return conn, nil
}

// Close closes all server connections
func (m *Manager) Close() error {
	// Use Swap to atomically set closed=true and get the previous value
	// This prevents TOCTOU race with CallTool's closed check
	if m.closed.Swap(true) {
		return nil // already closed
	}
	m.cancel()

	// Wait for all in-flight CallTool calls to finish before closing sessions
	// After closed=true is set, no new CallTool can start (they check closed first)
	m.wg.Wait()
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.log(logger.InfoCF, "mcp", "Closing all MCP server connections",
		map[string]any{
			"count": len(m.servers),
		})

	var errs []error
	for name, conn := range m.servers {
		conn.stopProcess()
		if err := conn.Session.Close(); err != nil {
			var exitErr *exec.ExitError
			if conn.command != nil && (errors.As(err, &exitErr) || errors.Is(err, context.Canceled)) {
				continue // Forced termination is expected for owned local processes.
			}
			m.log(logger.ErrorCF, "mcp", "Failed to close server connection",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
	}

	m.servers = make(map[string]*ServerConnection)

	if len(errs) > 0 {
		return fmt.Errorf("failed to close %d server(s): %w", len(errs), errors.Join(errs...))
	}

	return nil
}

// GetAllTools returns all tools from all connected servers
func (m *Manager) GetAllTools() map[string][]*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*mcp.Tool)
	for name, conn := range m.servers {
		if len(conn.Tools) > 0 {
			result[name] = conn.Tools
		}
	}
	return result
}

func (m *Manager) log(write func(string, string, map[string]any), component, message string, fields map[string]any) {
	if !m.options.QuietDiagnostics {
		write(component, message, fields)
	}
}

// ReadDeclaredEnvironment uses the same env-file parser and override order as
// local server startup. EnvFile must already be resolved relative to workspace.
func ReadDeclaredEnvironment(server config.MCPServerConfig) (map[string]string, error) {
	values := map[string]string{}
	if server.EnvFile != "" {
		var err error
		values, err = loadEnvFile(server.EnvFile)
		if err != nil {
			return nil, err
		}
	}
	for key, value := range server.Env {
		values[key] = value
	}
	return values, nil
}
