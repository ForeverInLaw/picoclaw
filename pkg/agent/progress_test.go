package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestTelegramToolProgressAndFinalReplyAreIndependent(t *testing.T) {
	for _, peer := range []string{"direct", "group"} {
		t.Run(peer, func(t *testing.T) {
			al, _, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			provider := &scriptedToolProvider{}
			a := al.registry.GetDefaultAgent()
			a.Provider = provider
			al.RegisterTool(&mockCustomTool{})
			response, err := al.runAgentLoop(
				context.Background(),
				a,
				processOptions{
					SessionKey:       "progress-" + peer,
					Channel:          "telegram",
					ChatID:           "-100:4",
					PeerKind:         peer,
					ReplyToMessageID: "19",
					UserMessage:      "run tool",
					SendResponse:     true,
				},
			)
			if err != nil || response != "done" {
				t.Fatalf("response=%q err=%v", response, err)
			}
			var delivered []bus.OutboundMessage
			for {
				select {
				case msg := <-mb.OutboundChan():
					delivered = append(delivered, msg)
				default:
					goto drained
				}
			}
		drained:
			if len(delivered) != 3 {
				t.Fatalf("expected start, completion, answer; got %+v", delivered)
			}
			start, end, answer := delivered[0], delivered[1], delivered[2]
			if start.Progress == nil || end.Progress == nil || answer.Progress != nil {
				t.Fatalf("progress must be separate from answer: %+v", delivered)
			}
			if start.Progress.State != bus.ProgressStarted || end.Progress.State != bus.ProgressCompleted ||
				end.Progress.Result == "" {
				t.Fatalf("missing start/outcome: %+v %+v", start.Progress, end.Progress)
			}
			if start.Progress.RequestID == "" || start.Progress.ActionID == "" ||
				start.Progress.RequestID != end.Progress.RequestID ||
				start.Progress.ActionID != end.Progress.ActionID {
				t.Fatal("tool invocation identity was not preserved")
			}
			if start.ChatID != "-100:4" || start.ReplyToMessageID != "19" || end.ReplyToMessageID != "19" ||
				answer.Content != "done" {
				t.Fatalf("lost routing or answer: %+v", delivered)
			}
		})
	}
}

type activityContractTool struct {
	mockCustomTool
	result *tools.ToolResult
}

func (m *activityContractTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return m.result
}

func TestRunToolActivityKeepsLLMOnlyResultsPrivate(t *testing.T) {
	for _, result := range []*tools.ToolResult{tools.SilentResult("PRIVATE_ARCHIVE_CONTEXT"), tools.ErrorResult("PRIVATE_CONTEXT_ERROR").WithError(errors.New("PRIVATE_INTERNAL_ERROR"))} {
		al, _, mb, _, cleanup := newTestAgentLoop(t)
		al.registry.GetDefaultAgent().Provider = &scriptedToolProvider{}
		al.RegisterTool(&activityContractTool{result: result})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- al.Run(ctx) }()
		publishIncoming(t, mb, directIngressMessage("perform action"))
		var completed *bus.ProgressMessage
		for {
			msg := <-mb.OutboundChan()
			if strings.Contains(msg.Content, "PRIVATE_") || (msg.Progress != nil && strings.Contains(msg.Progress.Result+msg.Progress.Error, "PRIVATE_")) {
				t.Errorf("private tool context was published: %+v", msg)
			}
			if msg.Progress == nil {
				break
			}
			if msg.Progress.State != bus.ProgressStarted {
				completed = msg.Progress
			}
		}
		if completed == nil || completed.Result == "" {
			t.Error("private tool must still produce a concise public completion")
		}
		cancel()
		<-done
		al.Close()
		mb.Close()
		cleanup()
	}
}

type activityFileProvider struct {
	calls     int
	tool      string
	arguments map[string]any
}

func (p *activityFileProvider) GetDefaultModel() string { return "activity" }
func (p *activityFileProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.calls++
	if p.tool == "" {
		p.tool = "read_file"
		p.arguments = map[string]any{"path": "public.txt"}
	}
	if p.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{ID: "file", Name: p.tool, Arguments: p.arguments}}}, nil
	}
	return &providers.LLMResponse{Content: "done"}, nil
}
func TestRunFileActivityRetainsExplicitOutputWithSensitiveDataFiltering(t *testing.T) {
	for _, channel := range []string{"telegram", "discord"} {
		t.Run(channel, func(t *testing.T) {
			al, cfg, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer al.Close()
			defer mb.Close()
			cfg.Tools.FilterSensitiveData = true
			cfg.Providers.OpenAI.APIKey = "SECRET_CONFIG_KEY"
			al.Close()
			directory := t.TempDir()
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "config.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, config.SecurityConfigFile), []byte("model_list:\n  test:\n    api_keys: [SECRET_CONFIG_KEY]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err = config.LoadConfig(filepath.Join(directory, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			al = NewAgentLoop(cfg, mb, &mockProvider{})
			defer al.Close()
			a := al.registry.GetDefaultAgent()
			a.Provider = &activityFileProvider{}
			al.RegisterTool(tools.NewReadFileTool(a.Workspace, true, 1<<20, nil))
			if err := os.WriteFile(filepath.Join(a.Workspace, "public.txt"), []byte("PUBLIC_FILE_OUTPUT SECRET_CONFIG_KEY"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- al.Run(ctx) }()
			defer func() { cancel(); <-done }()
			msg := directIngressMessage("read the file")
			msg.Channel = channel
			publishIncoming(t, mb, msg)
			public := false
			for {
				out := <-mb.OutboundChan()
				if strings.Contains(out.Content, "SECRET_CONFIG_KEY") {
					t.Errorf("activity leaked configured credential: %+v", out)
				}
				if out.Progress == nil {
					if out.Content != "done" {
						t.Errorf("activity generated an extra standalone message: %+v", out)
					}
					break
				}
				public = public || strings.Contains(out.Progress.Result, "PUBLIC_FILE_OUTPUT")
			}
			if channel == "telegram" && !public {
				t.Fatal("explicit file result disappeared from public activity")
			}
		})
	}
}

func TestRunSearchFailurePublishesExplicitSafeActivity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_api_key"}`))
	}))
	defer server.Close()
	search, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{ParallelEnabled: true, ParallelAPIKeys: []string{"test-key"}, ParallelBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	defer mb.Close()
	al.registry.GetDefaultAgent().Provider = &activityFileProvider{tool: "web_search", arguments: map[string]any{"query": "bicycle"}}
	al.RegisterTool(search)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	defer func() { cancel(); <-done }()
	publishIncoming(t, mb, directIngressMessage("search bicycle"))
	found := false
	for {
		out := <-mb.OutboundChan()
		if out.Progress == nil {
			break
		}
		if out.Progress.State == bus.ProgressFailed {
			found = strings.Contains(out.Progress.Result, "Search failed") && strings.Contains(out.Progress.Result, "401")
		}
	}
	if !found {
		t.Fatal("public search failure lost its useful provider/category detail")
	}
}
