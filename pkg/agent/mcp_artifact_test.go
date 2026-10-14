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
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const artifactFixtureSecret = "fixture-complete-secret-key"

func artifactScenarioConfig(t *testing.T, raw string, modify func(*config.Config)) (*config.Config, *atomic.Int32) {
	return artifactScenarioConfigWithGate(t, raw, modify, nil)
}

func artifactScenarioConfigWithGate(
	t *testing.T,
	raw string,
	modify func(*config.Config),
	gate func(),
) (*config.Config, *atomic.Int32) {
	t.Helper()
	executions := &atomic.Int32{}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "artifact-fixture", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "large", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			executions.Add(1)
			require.JSONEq(t, `{}`, string(request.Params.Arguments))
			if gate != nil {
				gate()
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: raw}}}, nil
		})
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	t.Cleanup(httpServer.Close)
	base := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(base, "workspace")
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.ContextWindow = 128000
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Agents.Defaults.SummarizeMessageThreshold = 100000
	cfg.Tools.FilterSensitiveData = false
	cfg.Tools.FilterMinLength = 1000000
	require.NoError(t, os.WriteFile(filepath.Join(base, "model.key"), []byte(artifactFixtureSecret), 0o600))
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "test-model", Model: "openai/test-model", APIKeys: []string{"file://model.key", "xy"}},
	}
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"fixture": {Enabled: true, Type: "http", URL: httpServer.URL},
	}
	cfg.Tools.MCP.Artifacts = config.MCPArtifactConfig{
		Directory: filepath.Join(base, "artifacts"), MaxInlineRunes: 256, PreviewRunes: 128,
	}
	if modify != nil {
		modify(cfg)
	}
	path := filepath.Join(base, "config.json")
	require.NoError(t, config.SaveConfig(path, cfg))
	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	return cfg, executions
}

func TestMCPArtifactQuotaIsSharedAcrossIndependentAgentLoops(t *testing.T) {
	var ready sync.WaitGroup
	ready.Add(2)
	cfg, executions := artifactScenarioConfigWithGate(t, strings.Repeat("x", 2*1024*1024), func(cfg *config.Config) {
		cfg.Tools.MCP.Artifacts.MaxDiskBytes = 3 * 1024 * 1024
	}, func() { ready.Done(); ready.Wait() })
	results := make(chan string, 2)
	for _, key := range []string{"one", "two"} {
		otherCfg := *cfg
		otherCfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
		p := &sourceScenarioProvider{tool: "mcp_fixture_large", args: map[string]any{}}
		al := NewAgentLoop(&otherCfg, bus.NewMessageBus(), p)
		t.Cleanup(al.Close)
		go func() {
			response, err := al.ProcessDirect(t.Context(), "query sources", "agent:main:cli:"+key)
			if err != nil {
				results <- err.Error()
				return
			}
			results <- response + p.expandOutput
		}()
	}
	stored := 0
	for range 2 {
		if strings.Contains(<-results, "artifact://mcp/") {
			stored++
		}
	}
	require.Equal(t, 1, stored, "shared disk quota must permit one 2 MiB result, never two")
	require.Equal(t, int32(2), executions.Load(), "completed operations cannot be replayed during storage contention")
}

func TestMCPArtifactPreviewAndReadRemainScopedAcrossReopenAndClear(t *testing.T) {
	raw := "xy " + strings.Repeat(
		"\u754c",
		110,
	) + " " + artifactFixtureSecret + strings.Repeat(
		" durable detail",
		2500,
	) + " END-OF-ARTIFACT"
	cfg, executions := artifactScenarioConfig(t, raw, nil)
	keys := make([]string, 0, len(cfg.ModelList))
	for _, model := range cfg.ModelList {
		keys = append(keys, model.APIKey)
	}
	require.Contains(t, keys, artifactFixtureSecret, "filter must protect decoded runtime credentials")
	p := &sourceScenarioProvider{tool: "mcp_fixture_large", args: map[string]any{}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer func() { al.Close() }()
	message := bus.InboundMessage{
		Channel: "telegram", ChatID: "42", SenderID: "42", Peer: bus.Peer{Kind: "direct", ID: "42"},
		Content: "query sources", SessionKey: "agent:main:telegram:direct:42",
	}
	process := func() (string, error) {
		return al.ProcessDirectWithChannel(
			t.Context(),
			message.Content,
			message.SessionKey,
			message.Channel,
			message.ChatID,
		)
	}
	_, err := process()
	require.NoError(t, err)
	preview := p.expandOutput
	require.Less(t, utf8.RuneCountInString(preview), 600)
	require.NotContains(t, preview, "fixture-complete")
	require.NotContains(t, preview, "fixture-", "preview must not expose even a cut decoded-key prefix")
	require.Contains(t, preview, "[FILTERED]")
	ref := regexp.MustCompile(`artifact://mcp/[a-f0-9]+`).FindString(preview)
	require.NotEmpty(t, ref)
	require.Equal(t, int32(1), executions.Load())
	files, err := filepath.Glob(filepath.Join(cfg.Tools.MCP.Artifacts.Directory, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	stored, err := os.ReadFile(files[0])
	require.NoError(t, err)
	var record struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(stored, &record))
	require.Equal(t, raw, record.Text, "durable raw record must remain available to explicit operator access")
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
	require.NotContains(t, p.expandOutput, artifactFixtureSecret)
	require.NotContains(t, p.expandOutput, "xy")
	// 110 three-byte runes after the first filtered key place the second
	// filtered token at byte 342. Paging must split the filtered view, not the key.
	for _, page := range []struct {
		offset, length int
		want           string
	}{
		{342, 4, "[FIL"}, {346, 6, "TERED]"},
	} {
		p.args = map[string]any{"path": ref, "offset": page.offset, "length": page.length}
		_, err = process()
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(p.expandOutput, page.want), p.expandOutput)
	}
	require.Equal(t, int32(1), executions.Load(), "explicit read must not replay MCP")
	for _, attempt := range []struct{ name, channel, chat, session, path string }{
		{"foreign conversation", "telegram", "43", "agent:main:telegram:direct:43", ref},
		{"physical record", "telegram", "43", "agent:main:telegram:direct:43", files[0]},
		{"caller-owned physical record", "telegram", "42", message.SessionKey, files[0]},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			p.args = map[string]any{"path": attempt.path, "agent_id": "main", "session_key": message.SessionKey}
			_, readErr := al.ProcessDirectWithChannel(
				t.Context(),
				"query sources",
				attempt.session,
				attempt.channel,
				attempt.chat,
			)
			require.NoError(t, readErr)
			require.Contains(t, p.expandOutput, "access denied")
			require.NotContains(t, p.expandOutput, "END-OF-ARTIFACT")
		})
	}
	alias := filepath.Join(cfg.Agents.Defaults.Workspace, "artifact-alias")
	require.NoError(t, os.Symlink(files[0], alias))
	p.args = map[string]any{"path": alias}
	_, err = al.ProcessDirectWithChannel(
		t.Context(),
		"query sources",
		"agent:main:telegram:direct:43",
		"telegram",
		"43",
	)
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "access denied")
	otherCfg := *cfg
	otherCfg.Agents.List = []config.AgentConfig{{ID: "other", Default: true}}
	other := NewAgentLoop(&otherCfg, bus.NewMessageBus(), p)
	p.args = map[string]any{"path": ref}
	_, err = other.ProcessDirectWithChannel(t.Context(), "query sources", message.SessionKey, "telegram", "42")
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "access denied")
	other.Close()
	namedCfg := *cfg
	namedCfg.ChannelList = config.ChannelList{
		"personal": &config.Channel{
			Type:     "telegram",
			Enabled:  true,
			Settings: json.RawMessage(`{"token":"fixture-token"}`),
		},
	}
	named := NewAgentLoop(&namedCfg, bus.NewMessageBus(), p)
	_, err = named.ProcessDirectWithChannel(t.Context(), "query sources", message.SessionKey, "personal", "42")
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "access denied", "named channel cannot spoof base channel SessionKey")
	named.Close()
	_, err = al.ProcessScheduled(t.Context(), tools.ScheduledRequest{
		Content: "query sources", SessionKey: message.SessionKey, Channel: "telegram", ChatID: "inline:42",
		Sender: bus.SenderInfo{CanonicalID: "telegram:42", Platform: "telegram", PlatformID: "42"},
	})
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "not available in this context")
	require.NotContains(t, p.expandOutput, "END-OF-ARTIFACT")
	require.Equal(t, int32(1), executions.Load())
	outside := filepath.Join(t.TempDir(), "private-record.json")
	require.NoError(t, os.WriteFile(outside, stored, 0o600))
	require.NoError(t, os.Remove(files[0]))
	require.NoError(t, os.Symlink(outside, files[0]))
	p.args = map[string]any{"path": ref}
	_, err = process()
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "unsafe")
	require.NotContains(
		t,
		p.expandOutput,
		"END-OF-ARTIFACT",
		"reference must not follow replaced records outside storage",
	)
}

type artifactReplayProvider struct{ calls atomic.Int32 }

func (p *artifactReplayProvider) GetDefaultModel() string { return "test-model" }

func (p *artifactReplayProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if len(messages) == 1 && messages[0].Role == "user" {
		return &providers.LLMResponse{Content: "summary"}, nil
	}
	p.calls.Add(1)
	return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
		{ID: "completed-operation", Name: "mcp_fixture_large", Arguments: map[string]any{}},
		{ID: "must-not-repeat", Name: "mcp_fixture_large", Arguments: map[string]any{}},
	}}, nil
}

type artifactWorkerTool struct{}

func (*artifactWorkerTool) Name() string               { return "artifact_worker" }
func (*artifactWorkerTool) Description() string        { return "Delegate an artifact query" }
func (*artifactWorkerTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*artifactWorkerTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	result, err := SpawnSubTurn(ctx, SubTurnConfig{Model: "test-model", SystemPrompt: "query sources"})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return result
}

type artifactWorkerProvider struct{ sourceScenarioProvider }

func (p *artifactWorkerProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	last := messages[len(messages)-1]
	if last.Role == "user" && last.Content == "delegate" {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{ID: "delegate", Name: "artifact_worker"}}}, nil
	}
	if last.Role == "tool" && last.ToolCallID == "delegate" {
		return &providers.LLMResponse{Content: "delegation complete"}, nil
	}
	return p.sourceScenarioProvider.Chat(ctx, messages, defs, model, opts)
}

func TestMCPArtifactSubturnUsesOwningBaseConversation(t *testing.T) {
	cfg, executions := artifactScenarioConfig(t, strings.Repeat("worker evidence ", 100), nil)
	p := &artifactWorkerProvider{
		sourceScenarioProvider: sourceScenarioProvider{tool: "mcp_fixture_large", args: map[string]any{}},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Close()
	al.GetRegistry().GetDefaultAgent().Tools.Register(&artifactWorkerTool{})
	const key = "agent:main:cli:direct:42"
	_, err := al.ProcessDirect(t.Context(), "delegate", key)
	require.NoError(t, err)
	ref := regexp.MustCompile(`artifact://mcp/[a-f0-9]+`).FindString(p.expandOutput)
	require.NotEmpty(t, ref, "subturn must capture the parent conversation rather than its ephemeral run ID")
	_, err = al.ProcessDirect(t.Context(), "/clear", key)
	require.NoError(t, err)
	p.tool, p.args = "read_file", map[string]any{"path": ref}
	_, err = al.ProcessDirect(t.Context(), "delegate", key)
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "worker evidence")
	_, err = al.ProcessDirect(t.Context(), "delegate", "agent:main:cli:direct:43")
	require.NoError(t, err)
	require.Contains(t, p.expandOutput, "access denied")
	require.Equal(t, int32(1), executions.Load())
}

func TestMCPArtifactQuotaFailureIsVisibleWithoutReplayingCompletedOperation(t *testing.T) {
	for _, problem := range []string{"quota", "write failure"} {
		for _, delivery := range []string{"direct", "outbound"} {
			t.Run(problem+"/"+delivery, func(t *testing.T) {
				cfg, executions := artifactScenarioConfig(
					t,
					strings.Repeat("completed-action ", 100),
					func(cfg *config.Config) {
						if problem == "quota" {
							cfg.Tools.MCP.Artifacts.MaxDiskBytes = 1
						} else {
							require.NoError(
								t,
								os.WriteFile(cfg.Tools.MCP.Artifacts.Directory, []byte("blocked"), 0o600),
							)
						}
					},
				)
				p := &artifactReplayProvider{}
				mb := bus.NewMessageBus()
				al := NewAgentLoop(cfg, mb, p)
				defer al.Close()
				defer mb.Close()
				var response string
				if delivery == "direct" {
					var err error
					response, err = al.ProcessDirect(t.Context(), "query sources", "agent:main:cli:direct:42")
					require.NoError(t, err)
				} else {
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() { done <- al.Run(ctx) }()
					defer func() { cancel(); require.NoError(t, <-done) }()
					publishIncoming(t, mb, directIngressMessage("query sources"))
					response = finalOutbound(t, mb).Content
				}
				require.Contains(t, response, "MCP operation completed")
				require.Contains(t, response, "could not be stored")
				if problem == "quota" {
					require.Contains(t, response, "disk quota exceeded")
				}
				require.NotContains(t, response, "completed-action")
				require.Equal(t, int32(1), executions.Load())
				require.Equal(t, int32(1), p.calls.Load(), "failure must terminate before model recovery/replay")
			})
		}
	}
}
