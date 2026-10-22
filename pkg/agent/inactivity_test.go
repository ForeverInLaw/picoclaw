package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type inactivityScriptProvider struct {
	calls           []string
	primaryCanceled bool
}

func (*inactivityScriptProvider) GetDefaultModel() string { return "primary" }

func (p *inactivityScriptProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls = append(p.calls, model)
	if model == "primary" {
		<-ctx.Done()
		p.primaryCanceled = errors.Is(ctx.Err(), context.Canceled)
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "backup completed"}, nil
}

func TestStalledModelYieldsToBackupInDirectAndInlineTurns(t *testing.T) {
	for _, chat := range []string{"42", "inline:chosen"} {
		t.Run(chat, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
				p := &inactivityScriptProvider{}
				a := al.registry.GetDefaultAgent()
				a.Provider = p
				a.Candidates = []providers.FallbackCandidate{
					{Provider: "openai", Model: "primary"},
					{Provider: "openai", Model: "backup"},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				started := time.Now()
				answer, err := al.processMessage(
					ctx,
					bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"},
				)
				if err != nil || !strings.HasSuffix(answer, "backup completed") ||
					strings.Join(p.calls, ",") != "primary,backup" ||
					!p.primaryCanceled ||
					ctx.Err() != nil ||
					time.Since(started) != time.Minute {
					t.Fatalf(
						"answer=%q err=%v calls=%v childCanceled=%t parent=%v elapsed=%v",
						answer,
						err,
						p.calls,
						p.primaryCanceled,
						ctx.Err(),
						time.Since(started),
					)
				}
			})
		})
	}
}

type inactivityRoundTripper func(*http.Request) (*http.Response, error)

func (f inactivityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type inactivityBody struct {
	*io.PipeReader
	stop func() bool
}

func (b inactivityBody) Close() error {
	b.stop()
	return b.PipeReader.Close()
}

func scriptedInactivityResponse(r *http.Request, script func(context.Context, io.Writer)) *http.Response {
	reader, writer := io.Pipe()
	stop := context.AfterFunc(r.Context(), func() { _ = reader.CloseWithError(r.Context().Err()) })
	go func() {
		defer writer.Close()
		script(r.Context(), writer)
	}()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       inactivityBody{reader, stop},
		Request:    r,
	}
}

func TestDecodedStreamProgressResetsInactivityAndStalledPreviewIsReplaced(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		chunk   string
		finish  bool
		elapsed time.Duration
	}{
		{"text_keeps_generating", `{"choices":[{"delta":{"content":"primary answer"}}]}`, true, 100 * time.Second},
		{"text_then_stall", `{"choices":[{"delta":{"content":"FAILED_PARTIAL"}}]}`, false, 110 * time.Second},
		{"reasoning_keeps_generating", `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`, true, 100 * time.Second},
		{"keepalive_cannot_extend_wait", `{"choices":[{"delta":{"role":"assistant"}}]}`, false, time.Minute},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, mb *bus.MessageBus) {
				calls := []string{}
				previous := http.DefaultTransport
				http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
					var input struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, input.Model)
					return scriptedInactivityResponse(r, func(ctx context.Context, w io.Writer) {
						if input.Model == "backup" {
							_, _ = fmt.Fprint(
								w,
								"data: {\"choices\":[{\"delta\":{\"content\":\"backup answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
							)
							return
						}
						select {
						case <-time.After(50 * time.Second):
						case <-ctx.Done():
							return
						}
						_, _ = fmt.Fprintf(w, "data: %s\n\n", scenario.chunk)
						if !scenario.finish {
							<-ctx.Done()
							return
						}
						select {
						case <-time.After(50 * time.Second):
						case <-ctx.Done():
							return
						}
						_, _ = fmt.Fprint(
							w,
							"data: {\"choices\":[{\"delta\":{\"content\":\" completed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
						)
					}), nil
				})
				defer func() { http.DefaultTransport = previous }()
				a := al.registry.GetDefaultAgent()
				a.Provider = providers.NewHTTPProvider("", "https://model.test", "")
				a.Candidates = []providers.FallbackCandidate{
					{Provider: "openai", Model: "primary"},
					{Provider: "openai", Model: "backup"},
				}
				delegate := &attemptStreamDelegate{}
				mb.SetStreamDelegate(delegate)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				started := time.Now()
				answer, err := al.processMessage(
					ctx,
					bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Content: "hello"},
				)
				if err != nil || time.Since(started) != scenario.elapsed {
					t.Fatalf("answer=%q err=%v calls=%v elapsed=%v", answer, err, calls, time.Since(started))
				}
				if scenario.finish {
					if len(calls) != 1 || !strings.HasSuffix(answer, "completed") {
						t.Fatalf("progress lost: answer=%q calls=%v", answer, calls)
					}
				} else if answer != "backup answer" || strings.Join(calls, ",") != "primary,backup" || delegate.streams[0].canceled != 1 || len(delegate.streams[0].finalized) != 0 {
					t.Fatalf(
						"failed preview leaked: answer=%q calls=%v preview=%+v",
						answer,
						calls,
						delegate.streams[0],
					)
				}
			})
		})
	}
}

func TestInactivityChangesApplyToNextCapturedRequest(t *testing.T) {
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: "https://model.test", InactivityTimeoutSeconds: 30},
		{ModelName: "backup", Model: "openai/backup", APIBase: "https://model.test"},
	}, []string{"backup"})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, al.cfg); err != nil {
		t.Fatal(err)
	}
	al.SetRoutingConfigSource(func() (*config.Config, error) { return config.LoadConfig(path) })
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := []string{}
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			var input struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, input.Model)
			if input.Model == "primary" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(
					strings.NewReader(`{"choices":[{"message":{"content":"backup answer"},"finish_reason":"stop"}]}`),
				),
				Request: r,
			}, nil
		})
		go func() {
			time.Sleep(5 * time.Second)
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Error(err)
				return
			}
			cfg.ModelList[0].InactivityTimeoutSeconds = 7
			if err := config.SaveConfig(path, cfg); err != nil {
				t.Error(err)
			}
		}()
		for i, expected := range []time.Duration{30 * time.Second, 7 * time.Second} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			start := time.Now()
			answer, err := al.ProcessDirect(ctx, "hello", fmt.Sprintf("capture-%d", i))
			cancel()
			if err != nil || answer != "backup answer" || time.Since(start) != expected {
				t.Fatalf("request%d answer=%q err=%v elapsed=%v", i, answer, err, time.Since(start))
			}
			if i == 0 {
				time.Sleep(time.Hour)
			} // Existing cooldown expires before fresh priority is considered.
		}
		if strings.Join(calls, ",") != "primary,backup,primary,backup" {
			t.Fatalf("calls=%v", calls)
		}
	})
}

func TestCodexReasoningAndToolFormationKeepNoPreviewRequestsAlive(t *testing.T) {
	for _, progress := range []string{
		`{"type":"response.output_text.delta","delta":"working"}`,
		`{"type":"response.reasoning_text.delta","delta":"thinking"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"response.function_call_arguments.delta","delta":"{"}`,
		`{"type":"response.custom_tool_call_input.delta","delta":"work"}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"f1","call_id":"c1","name":"web_fetch","arguments":""}}`,
	} {
		t.Run(progress, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
				previous := http.DefaultTransport
				defer func() { http.DefaultTransport = previous }()
				calls := 0
				http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					return scriptedInactivityResponse(r, func(ctx context.Context, w io.Writer) {
						select {
						case <-time.After(50 * time.Second):
						case <-ctx.Done():
							return
						}
						_, _ = fmt.Fprintf(w, "data: %s\n\n", progress)
						select {
						case <-time.After(50 * time.Second):
						case <-ctx.Done():
							return
						}
						_, _ = fmt.Fprint(
							w,
							"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"completed\"}]}]}}\n\n",
						)
					}), nil
				})
				p := providers.NewCodexProvider("test-token", "")
				p.SetExactModel(true)
				a := al.registry.GetDefaultAgent()
				a.Provider = p
				a.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "primary"}}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				start := time.Now()
				answer, err := al.ProcessDirect(ctx, "hello", "codex-no-preview")
				if err != nil || answer != "completed" || calls != 1 || time.Since(start) != 100*time.Second {
					t.Fatalf("answer=%q err=%v calls=%d elapsed=%v", answer, err, calls, time.Since(start))
				}
			})
		})
	}
}

func TestStalledCompactionCandidateYieldsWithoutLosingCanonicalConversation(t *testing.T) {
	initial, cfg := newSeahorseScenarioLoop(t, &sourceScenarioProvider{})
	initial.Close()
	cfg.Agents.Defaults.Model = "primary"
	cfg.Agents.Defaults.ModelFallbacks = []string{"backup"}
	cfg.Agents.Defaults.MaxTokens = 123
	cfg.Agents.Defaults.ContextWindow = 12000
	cfg.Agents.Defaults.SummarizeTokenPercent = 100
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: "https://model.test", InactivityTimeoutSeconds: 5},
		{ModelName: "backup", Model: "openai/backup", APIBase: "https://model.test"},
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	al.SetRoutingConfigSource(func() (*config.Config, error) { return cfg, nil })
	agent := al.registry.GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	for range 8 {
		agent.Sessions.AddFullMessage(
			key,
			providers.Message{Role: "user", Content: strings.Repeat("ORIGINAL_EVIDENCE ", 1500)},
		)
		agent.Sessions.AddFullMessage(key, providers.Message{Role: "assistant", Content: "retained answer"})
	}
	synctest.Test(t, func(t *testing.T) {
		defer al.Close()
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		primaryCalls, summaries := 0, 0
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			var input struct {
				Model    string              `json:"model"`
				Messages []providers.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Model == "primary" {
				primaryCalls++
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			if len(input.Messages) == 1 && input.Messages[0].Role == "user" {
				summaries++
				time.Sleep(time.Millisecond)
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(
					strings.NewReader(
						`{"choices":[{"message":{"content":"retained summary and answer"},"finish_reason":"stop"}]}`,
					),
				),
				Request: r,
			}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		answer, err := al.processMessage(ctx, selectedInlineMessage("42", "compaction-timeout", "next"))
		if err != nil || !strings.Contains(answer, "retained summary and answer") || primaryCalls != 1 ||
			summaries == 0 ||
			(time.Since(start) < 5*time.Second || time.Since(start) >= 6*time.Second) ||
			len(agent.Sessions.GetHistory(key)) < 18 {
			t.Fatalf(
				"answer=%q err=%v primary=%d summaries=%d elapsed=%v history=%d",
				answer,
				err,
				primaryCalls,
				summaries,
				time.Since(start),
				len(agent.Sessions.GetHistory(key)),
			)
		}
	})
}

func TestAnthropicReasoningKeepsNoPreviewRequestAlive(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := 0
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			return scriptedInactivityResponse(r, func(ctx context.Context, w io.Writer) {
				_, _ = fmt.Fprint(
					w,
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"primary\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n",
				)
				select {
				case <-time.After(50 * time.Second):
				case <-ctx.Done():
					return
				}
				_, _ = fmt.Fprint(
					w,
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"working\"}}\n\n",
				)
				select {
				case <-time.After(50 * time.Second):
				case <-ctx.Done():
					return
				}
				_, _ = fmt.Fprint(
					w,
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"completed\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
				)
			}), nil
		})
		p := providers.NewClaudeProviderWithTokenSource(
			"test-token",
			func() (string, error) { return "test-token", nil },
		)
		a := al.registry.GetDefaultAgent()
		a.Provider = p
		a.Candidates = []providers.FallbackCandidate{{Provider: "anthropic", Model: "primary"}}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		start := time.Now()
		answer, err := al.ProcessDirect(ctx, "hello", "anthropic-no-preview")
		if err != nil || answer != "completed" || calls != 1 || time.Since(start) != 100*time.Second {
			t.Fatalf("answer=%q err=%v calls=%d elapsed=%v", answer, err, calls, time.Since(start))
		}
	})
}

type slowInactivityPreview struct{ fakeStreamingSink }

func (s *slowInactivityPreview) Update(ctx context.Context, content string) error {
	time.Sleep(2 * time.Second)
	return s.fakeStreamingSink.Update(ctx, content)
}

type slowInactivityDelegate struct{ streams []*slowInactivityPreview }

func (d *slowInactivityDelegate) GetStreamer(context.Context, string, string) (bus.Streamer, bool) {
	s := &slowInactivityPreview{}
	d.streams = append(d.streams, s)
	return s, true
}

func TestSlowPreviewDeliveryDoesNotConsumeModelInactivity(t *testing.T) {
	al, mb := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: "https://model.test", InactivityTimeoutSeconds: 1},
		{ModelName: "backup", Model: "openai/backup", APIBase: "https://model.test"},
	}, []string{"backup"})
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := 0
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			return scriptedInactivityResponse(r, func(ctx context.Context, w io.Writer) {
				select {
				case <-time.After(500 * time.Millisecond):
				case <-ctx.Done():
					return
				}
				_, _ = fmt.Fprint(
					w,
					"data: {\"choices\":[{\"delta\":{\"content\":\"completed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
				)
			}), nil
		})
		delegate := &slowInactivityDelegate{}
		mb.SetStreamDelegate(delegate)
		answer, err := al.processMessage(context.Background(), directIngressMessage("hello"))
		if err != nil || answer != "completed" || calls != 1 || len(delegate.streams) != 1 ||
			delegate.streams[0].canceled != 0 {
			t.Fatalf("answer=%q err=%v calls=%d previews=%+v", answer, err, calls, delegate.streams)
		}
	})
}

func TestAntigravityDecodedStreamKeepsNoPreviewRequestAlive(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential(
		"google-antigravity",
		&auth.AuthCredential{
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
			AccessToken: "test-token",
			ProjectID:   "test-project",
		},
	); err != nil {
		t.Fatal(err)
	}
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := 0
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			return scriptedInactivityResponse(r, func(ctx context.Context, w io.Writer) {
				select {
				case <-time.After(50 * time.Second):
				case <-ctx.Done():
					return
				}
				_, _ = fmt.Fprint(
					w,
					"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"working \"}]}}]}}\n\n",
				)
				select {
				case <-time.After(50 * time.Second):
				case <-ctx.Done():
					return
				}
				_, _ = fmt.Fprint(
					w,
					"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"completed\"}]},\"finishReason\":\"STOP\"}]}}\n\ndata: [DONE]\n\n",
				)
			}), nil
		})
		a := al.registry.GetDefaultAgent()
		a.Provider = providers.NewAntigravityProvider()
		a.Candidates = []providers.FallbackCandidate{{Provider: "antigravity", Model: "primary"}}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		start := time.Now()
		answer, err := al.ProcessDirect(ctx, "hello", "antigravity-no-preview")
		if err != nil || answer != "working completed" || calls != 1 || time.Since(start) != 100*time.Second {
			t.Fatalf("answer=%q err=%v calls=%d elapsed=%v", answer, err, calls, time.Since(start))
		}
	})
}

type inactivityProviderFunc func(context.Context, []providers.Message, string) (*providers.LLMResponse, error)

func (inactivityProviderFunc) GetDefaultModel() string { return "primary" }

func (f inactivityProviderFunc) Chat(
	ctx context.Context,
	m []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return f(ctx, m, model)
}

type slowInactivityTool struct{ recordingActionTool }

func (s *slowInactivityTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	select {
	case <-time.After(90 * time.Second):
		return s.recordingActionTool.Execute(ctx, args)
	case <-ctx.Done():
		return tools.ErrorResult(ctx.Err().Error())
	}
}

func TestLongLocalToolKeepsCompletedResultWhenNextModelStalls(t *testing.T) {
	for _, chat := range []string{"42", "inline:chosen"} {
		t.Run(chat, func(t *testing.T) {
			withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
				tool := &slowInactivityTool{recordingActionTool: recordingActionTool{name: "web_fetch"}}
				al.RegisterTool(tool)
				calls := []string{}
				retained := false
				p := inactivityProviderFunc(
					func(ctx context.Context, m []providers.Message, model string) (*providers.LLMResponse, error) {
						calls = append(calls, model)
						if len(calls) == 1 {
							return &providers.LLMResponse{
								ToolCalls: []providers.ToolCall{
									{ID: "once", Name: "web_fetch", Arguments: map[string]any{}},
								},
							}, nil
						}
						if model == "primary" {
							<-ctx.Done()
							return nil, ctx.Err()
						}
						for _, message := range m {
							if message.Role == "tool" && strings.Contains(message.Content, "action completed once") {
								retained = true
							}
						}
						return &providers.LLMResponse{Content: "backup completed"}, nil
					},
				)
				a := al.registry.GetDefaultAgent()
				a.Provider = p
				a.Candidates = []providers.FallbackCandidate{
					{Provider: "openai", Model: "primary"},
					{Provider: "openai", Model: "backup"},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				start := time.Now()
				answer, err := al.processMessage(
					ctx,
					bus.InboundMessage{
						Channel:  "telegram",
						ChatID:   chat,
						SenderID: "telegram:42",
						Content:  "perform action",
					},
				)
				if err != nil || !strings.HasSuffix(answer, "backup completed") || tool.executions != 1 || !retained ||
					strings.Join(calls, ",") != "primary,primary,backup" ||
					time.Since(start) != 150*time.Second {
					t.Fatalf(
						"answer=%q err=%v calls=%v tools=%d retained=%t elapsed=%v",
						answer,
						err,
						calls,
						tool.executions,
						retained,
						time.Since(start),
					)
				}
			})
		})
	}
}

func TestCancelStalledAttemptStopsWithoutCallingReserve(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
		p := &inactivityScriptProvider{}
		a := al.registry.GetDefaultAgent()
		a.Provider = p
		a.Candidates = []providers.FallbackCandidate{
			{Provider: "openai", Model: "primary"},
			{Provider: "openai", Model: "backup"},
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { time.Sleep(10 * time.Second); cancel() }()
		start := time.Now()
		answer, err := al.ProcessDirect(ctx, "hello", "cancel-stall")
		if answer != "" || !errors.Is(err, context.Canceled) || len(p.calls) != 1 ||
			time.Since(start) != 10*time.Second {
			t.Fatalf("answer=%q err=%v calls=%v elapsed=%v", answer, err, p.calls, time.Since(start))
		}
	})
}

func TestStalledSubTurnGetsItsOwnLiveReserveRequest(t *testing.T) {
	withRetryLoop(t, func(t *testing.T, al *AgentLoop, _ *bus.MessageBus) {
		p := &inactivityScriptProvider{}
		a := al.registry.GetDefaultAgent()
		a.Provider = p
		a.Candidates = []providers.FallbackCandidate{
			{Provider: "openai", Model: "primary"},
			{Provider: "openai", Model: "backup"},
		}
		parent := &turnState{
			ctx:            context.Background(),
			turnID:         "parent",
			childTurnIDs:   []string{},
			pendingResults: make(chan *tools.ToolResult, 10),
			session:        &ephemeralSessionStore{},
			agent:          a,
		}
		start := time.Now()
		result, err := spawnSubTurn(
			context.Background(),
			al,
			parent,
			SubTurnConfig{Model: "primary", Tools: []tools.Tool{}, SystemPrompt: "hello"},
		)
		if err != nil || result == nil || !strings.Contains(result.ForLLM, "backup completed") ||
			strings.Join(p.calls, ",") != "primary,backup" ||
			time.Since(start) != time.Minute {
			t.Fatalf("result=%+v err=%v calls=%v elapsed=%v", result, err, p.calls, time.Since(start))
		}
	})
}

func TestStalledCodexRefreshIsCanceledBeforeIndependentAccountServes(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	for _, id := range []string{"a", "b"} {
		credential := &auth.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "token-" + id}
		if id == "a" {
			credential.RefreshToken = "refresh-a"
			credential.ExpiresAt = time.Unix(1, 0)
		}
		if err := auth.SaveAccount(
			&auth.AccountConnection{
				ID:         id,
				Provider:   "openai",
				Enabled:    true,
				Resource:   "https://model.test/v1",
				Scopes:     auth.OpenAIPlanScope,
				OAuth:      auth.OAuthProviderConfig{ClientID: "client-" + id, TokenURL: "https://auth.test/" + id},
				Credential: credential,
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "a", Model: "openai/shared", AuthMethod: "oauth", AccountID: "a", InactivityTimeoutSeconds: 5},
		{ModelName: "b", Model: "openai/shared", AuthMethod: "oauth", AccountID: "b"},
	}, []string{"b"})
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		refreshes, inferences := 0, 0
		refreshCanceled := false
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "auth.test" {
				refreshes++
				<-r.Context().Done()
				refreshCanceled = errors.Is(r.Context().Err(), context.Canceled)
				return nil, r.Context().Err()
			}
			inferences++
			if r.Header.Get("Authorization") != "Bearer token-b" {
				t.Errorf("canceled account sent inference or credentials mixed")
			}
			return scriptedInactivityResponse(r, func(_ context.Context, w io.Writer) {
				_, _ = fmt.Fprint(
					w,
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"independent account completed\"}]}]}}\n\n",
				)
			}), nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		start := time.Now()
		answer, err := al.ProcessDirect(ctx, "hello", "refresh-timeout")
		if err != nil || answer != "independent account completed" || refreshes != 1 || inferences != 1 ||
			!refreshCanceled ||
			time.Since(start) != 5*time.Second ||
			ctx.Err() != nil {
			t.Fatalf(
				"answer=%q err=%v refreshes=%d inference=%d canceled=%t parent=%v elapsed=%v",
				answer,
				err,
				refreshes,
				inferences,
				refreshCanceled,
				ctx.Err(),
				time.Since(start),
			)
		}
	})
	for _, id := range []string{"a", "b"} {
		account, err := auth.GetAccount(id)
		if err != nil || account.Credential == nil || account.Credential.AccessToken != "token-"+id {
			t.Fatalf("canceled refresh changed account%s: account=%+v err=%v", id, account, err)
		}
	}
}

func TestAntigravityTruncatedStreamYieldsToReserve(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential(
		"google-antigravity",
		&auth.AuthCredential{
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
			AccessToken: "test-token",
			ProjectID:   "test-project",
		},
	); err != nil {
		t.Fatal(err)
	}
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "antigravity/shared"},
		{ModelName: "backup", Model: "openai/reserve", APIBase: "https://model.test", APIKey: "backup-key"},
	}, []string{"backup"})
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := []string{}
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Host)
			if r.URL.Host == "cloudcode-pa.googleapis.com" {
				return scriptedInactivityResponse(r, func(_ context.Context, w io.Writer) {
					_, _ = fmt.Fprint(
						w,
						"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"FAILED_PARTIAL\"}]}}]}}\n\n",
					)
				}), nil
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "FAILED_PARTIAL") ||
				r.Header.Get("Authorization") != "Bearer backup-key" {
				t.Fatalf("partial or credential leaked into reserve: %s", body)
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(
					strings.NewReader(`{"choices":[{"message":{"content":"coherent backup"},"finish_reason":"stop"}]}`),
				),
				Request: r,
			}, nil
		})
		answer, err := al.ProcessDirect(context.Background(), "hello", "antigravity-truncated")
		if err != nil || answer != "coherent backup" ||
			strings.Join(calls, ",") != "cloudcode-pa.googleapis.com,model.test" {
			t.Fatalf("answer=%q err=%v calls=%v", answer, err, calls)
		}
	})
}

func TestAnthropicTruncatedStreamYieldsToReserve(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential(
		"anthropic",
		&auth.AuthCredential{Provider: "anthropic", AuthMethod: "oauth", AccessToken: "test-token"},
	); err != nil {
		t.Fatal(err)
	}
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "anthropic/shared", AuthMethod: "oauth"},
		{ModelName: "backup", Model: "openai/reserve", APIBase: "https://model.test", APIKey: "backup-key"},
	}, []string{"backup"})
	synctest.Test(t, func(t *testing.T) {
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		calls := []string{}
		http.DefaultTransport = inactivityRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Host)
			if r.URL.Host == "api.anthropic.com" {
				return scriptedInactivityResponse(r, func(_ context.Context, w io.Writer) {
					_, _ = fmt.Fprint(
						w,
						"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"shared\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"FAILED_PARTIAL\"}}\n\n",
					)
				}), nil
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(
					strings.NewReader(`{"choices":[{"message":{"content":"coherent backup"},"finish_reason":"stop"}]}`),
				),
				Request: r,
			}, nil
		})
		answer, err := al.ProcessDirect(context.Background(), "hello", "anthropic-truncated")
		if err != nil || answer != "coherent backup" || strings.Join(calls, ",") != "api.anthropic.com,model.test" {
			t.Fatalf("answer=%q err=%v calls=%v", answer, err, calls)
		}
	})
}
