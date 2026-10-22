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

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type inactivityScriptProvider struct {
	calls           []string
	primaryCanceled bool
}

func (*inactivityScriptProvider) GetDefaultModel() string { return "primary" }
func (p *inactivityScriptProvider) Chat(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, model string, _ map[string]any) (*providers.LLMResponse, error) {
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
				a.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "primary"}, {Provider: "openai", Model: "backup"}}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				started := time.Now()
				answer, err := al.processMessage(ctx, bus.InboundMessage{Channel: "telegram", ChatID: chat, SenderID: "telegram:42", Content: "hello"})
				if err != nil || !strings.HasSuffix(answer, "backup completed") || strings.Join(p.calls, ",") != "primary,backup" || !p.primaryCanceled || ctx.Err() != nil || time.Since(started) != time.Minute {
					t.Fatalf("answer=%q err=%v calls=%v childCanceled=%t parent=%v elapsed=%v", answer, err, p.calls, p.primaryCanceled, ctx.Err(), time.Since(started))
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
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: inactivityBody{reader, stop}, Request: r}
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
							_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"backup answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
						_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" completed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}), nil
				})
				defer func() { http.DefaultTransport = previous }()
				a := al.registry.GetDefaultAgent()
				a.Provider = providers.NewHTTPProvider("", "https://model.test", "")
				a.Candidates = []providers.FallbackCandidate{{Provider: "openai", Model: "primary"}, {Provider: "openai", Model: "backup"}}
				delegate := &attemptStreamDelegate{}
				mb.SetStreamDelegate(delegate)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				started := time.Now()
				answer, err := al.processMessage(ctx, bus.InboundMessage{Channel: "telegram", ChatID: "42", SenderID: "telegram:42", Content: "hello"})
				if err != nil || time.Since(started) != scenario.elapsed {
					t.Fatalf("answer=%q err=%v calls=%v elapsed=%v", answer, err, calls, time.Since(started))
				}
				if scenario.finish {
					if len(calls) != 1 || !strings.HasSuffix(answer, "completed") {
						t.Fatalf("progress lost: answer=%q calls=%v", answer, calls)
					}
				} else if answer != "backup answer" || strings.Join(calls, ",") != "primary,backup" || delegate.streams[0].canceled != 1 || len(delegate.streams[0].finalized) != 0 {
					t.Fatalf("failed preview leaked: answer=%q calls=%v preview=%+v", answer, calls, delegate.streams[0])
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
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"backup answer"},"finish_reason":"stop"}]}`)), Request: r}, nil
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
						_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"completed\"}]}]}}\n\n")
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
