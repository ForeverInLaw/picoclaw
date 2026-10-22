package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type failoverSearchTool struct{ mockCustomTool }

func (*failoverSearchTool) Name() string { return "web_search" }

type nativeSearchFailureProvider struct{}

func (*nativeSearchFailureProvider) GetDefaultModel() string    { return "primary" }
func (*nativeSearchFailureProvider) SupportsNativeSearch() bool { return true }

func (*nativeSearchFailureProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return nil, fmt.Errorf("status: 401 invalid API key")
}

func TestFailoverRestoresClientSearchForCandidateWithoutNativeSearch(t *testing.T) {
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []providers.ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		hasSearch := false
		for _, tool := range request.Tools {
			hasSearch = hasSearch || tool.Function.Name == "web_search"
		}
		if !hasSearch {
			t.Error("fallback must retain a usable search surface")
		}
		_, _ = w.Write(
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"search retained"},"finish_reason":"stop"}]}`),
		)
	}))
	defer backup.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: backup.URL},
		{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
	}, []string{"backup"})
	al.RegisterTool(&failoverSearchTool{})
	al.cfg.Tools.Web.PreferNative = true
	al.registry.GetDefaultAgent().Provider = &nativeSearchFailureProvider{}
	answer, err := al.ProcessDirect(t.Context(), "Search the web", "search-failover")
	require.NoError(t, err)
	require.Equal(t, "search retained", answer)
}

func TestFailoverSkipsIncompatibleCandidatesWithoutDroppingActiveContext(t *testing.T) {
	for _, scenario := range []struct{ name, capabilities, reason string }{
		{"images", `{"images":false}`, "image"},
		{"historical-images", `{"images":false}`, "image"},
		{"tools", `{"tools":false}`, "tool"},
		{"context", `{"context_window":1}`, "context"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			skipped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"incompatible candidate was called"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer skipped.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				require.Contains(t, string(request.Messages), "ACTIVE_EVIDENCE")
				require.Contains(t, string(request.Messages), "data:image/png;base64,aW1hZ2U=")
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"compatible answer"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer backup.Close()
			var incompatible config.ModelConfig
			require.NoError(
				t,
				json.Unmarshal(
					[]byte(
						`{"model_name":"limited","model":"openai/limited","capabilities":`+scenario.capabilities+`}`,
					),
					&incompatible,
				),
			)
			incompatible.APIBase = skipped.URL
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{ModelName: "primary", Model: "openai/primary", APIBase: backup.URL},
				incompatible,
				{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
			}, []string{"limited", "backup"})
			al.registry.GetDefaultAgent().Provider = &nativeSearchFailureProvider{}
			events := al.SubscribeEvents(64)
			defer al.UnsubscribeEvents(events.ID)
			msg := directIngressMessage("ACTIVE_EVIDENCE: describe image")
			msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
			if scenario.name == "historical-images" {
				al.registry.GetDefaultAgent().Provider = &recordingProvider{}
				_, err := al.processMessage(t.Context(), msg)
				require.NoError(t, err)
				al.registry.GetDefaultAgent().Provider = &nativeSearchFailureProvider{}
				msg.Content, msg.Media = "Describe the earlier image", nil
			}
			answer, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Equal(t, "compatible answer", answer)
			require.Zero(t, calls)
			foundReason := false
			for {
				select {
				case event := <-events.C:
					if event.Kind == EventKindLLMRetry {
						payload := event.Payload.(LLMRetryPayload)
						foundReason = foundReason ||
							payload.Reason == "incompatible" && strings.Contains(payload.Error, scenario.reason)
					}
				default:
					require.True(t, foundReason, "operator must be told why a candidate was skipped")
					return
				}
			}
		})
	}
}

type failoverActionTool struct{ recordingActionTool }

func (*failoverActionTool) Name() string { return "web_fetch" }

func TestFailoverReplacesTruncatedStreamAndRetainsCompletedWorkInDirectAndInlineTurns(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			primaryCalls, backupCalls := 0, 0
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls++
				w.Header().Set("Content-Type", "text/event-stream")
				if primaryCalls == 1 {
					_, _ = fmt.Fprint(
						w,
						"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"completed-action\",\"type\":\"function\",\"function\":{\"name\":\"web_fetch\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n",
					)
					return
				}
				// A clean HTTP EOF is still incomplete when the model never ends its stream.
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"FAILED_PARTIAL_ANSWER\"}}]}\n\n")
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls++
				var request struct {
					Messages []struct {
						Role       string               `json:"role"`
						Content    json.RawMessage      `json:"content"`
						ToolCallID string               `json:"tool_call_id"`
						ToolCalls  []providers.ToolCall `json:"tool_calls"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				encoded, _ := json.Marshal(request.Messages)
				require.Contains(t, string(encoded), "action completed once")
				require.Contains(t, string(encoded), "completed-action")
				require.Contains(t, string(encoded), "data:image/png;base64,aW1hZ2U=")
				require.NotContains(t, string(encoded), "FAILED_PARTIAL_ANSWER")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(
					w,
					"data: {\"choices\":[{\"delta\":{\"content\":\"Coherent backup answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
				)
			}))
			defer backup.Close()
			al, mb := configuredProviderLoop(t, []config.ModelConfig{
				{ModelName: "primary", Model: "openai/primary", APIBase: primary.URL},
				{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
			}, []string{"backup"})
			tool := &failoverActionTool{}
			al.RegisterTool(tool)
			delegate := &attemptStreamDelegate{}
			mb.SetStreamDelegate(delegate)
			msg := directIngressMessage("Perform the action and describe image")
			if inline {
				msg = selectedInlineMessage("42", "published", "Perform the action and describe image")
			}
			msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
			answer, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			expectedAnswer, expectedPreview := "Coherent backup answer", "FAILED_PARTIAL_ANSWER"
			if inline {
				expectedAnswer = "> Perform the action and describe image\n\nCoherent backup answer"
				expectedPreview = "> Perform the action and describe image\n\nFAILED_PARTIAL_ANSWER"
			}
			require.Equal(t, expectedAnswer, answer)
			require.Equal(t, 1, tool.executions)
			require.Equal(t, 1, backupCalls)
			require.Len(t, delegate.streams, 3)
			failed := delegate.streams[1]
			require.Equal(t, []string{expectedPreview}, failed.updates)
			require.Equal(t, 1, failed.canceled)
			require.Empty(t, failed.finalized)
			require.Equal(t, []string{expectedAnswer}, delegate.streams[2].finalized)
		})
	}
}

func TestFailoverRetainsConversationResetAndInlineIsolation(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				input := string(request.Messages)
				require.NotContains(t, input, "OLD_DIRECT_DIALOG")
				require.NotContains(t, input, "OLD_INLINE_DIALOG")
				if inline {
					require.NotContains(t, input, "SAVED_BICYCLE_FACT")
				} else {
					require.Contains(t, input, "SAVED_BICYCLE_FACT")
				}
				_, _ = fmt.Fprint(
					w,
					`{"choices":[{"message":{"role":"assistant","content":"isolated backup answer"},"finish_reason":"stop"}]}`,
				)
			}))
			defer backup.Close()
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{ModelName: "primary", Model: "openai/primary", APIBase: backup.URL},
				{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
			}, []string{"backup"})
			agent := al.registry.GetDefaultAgent()
			agent.Provider = &recordingProvider{}
			require.NoError(t, agent.ContextBuilder.memory.WriteLongTerm("# Saved facts\n\nSAVED_BICYCLE_FACT"))
			direct := directIngressMessage("OLD_DIRECT_DIALOG")
			_, err := al.processMessage(t.Context(), direct)
			require.NoError(t, err)
			_, err = al.processMessage(t.Context(), selectedInlineMessage("42", "before-reset", "OLD_INLINE_DIALOG"))
			require.NoError(t, err)
			reset := direct
			reset.Content = "/clear"
			if inline {
				reset.Content = "/clear_inline"
			}
			_, err = al.processMessage(t.Context(), reset)
			require.NoError(t, err)
			agent.Provider = &nativeSearchFailureProvider{}
			msg := directIngressMessage("Answer after reset")
			expected := "isolated backup answer"
			if inline {
				msg = selectedInlineMessage("42", "after-reset", "Answer after reset")
				expected = "> Answer after reset\n\nisolated backup answer"
			}
			answer, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Equal(t, expected, answer)
		})
	}
}

type textOnlyImageProvider struct{ calls int }

func (*textOnlyImageProvider) GetDefaultModel() string  { return "primary" }
func (*textOnlyImageProvider) SupportsImageInput() bool { return false }

func (p *textOnlyImageProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	return &providers.LLMResponse{Content: "image was silently omitted"}, nil
}

func TestFailoverSkipsAdapterThatCannotTransmitImageInput(t *testing.T) {
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		require.Contains(t, string(request.Messages), "data:image/png;base64,aW1hZ2U=")
		_, _ = fmt.Fprint(
			w,
			`{"choices":[{"message":{"role":"assistant","content":"image preserved"},"finish_reason":"stop"}]}`,
		)
	}))
	defer backup.Close()
	al, _ := configuredProviderLoop(t, []config.ModelConfig{
		{ModelName: "primary", Model: "openai/primary", APIBase: backup.URL},
		{ModelName: "backup", Model: "openai/backup", APIBase: backup.URL},
	}, []string{"backup"})
	primary := &textOnlyImageProvider{}
	al.registry.GetDefaultAgent().Provider = primary
	msg := directIngressMessage("Describe image")
	msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
	answer, err := al.processMessage(t.Context(), msg)
	require.NoError(t, err)
	require.Equal(t, "image preserved", answer)
	require.Zero(t, primary.calls)
}
