package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

type routingModelHook struct{ calls int }

func (*routingModelHook) AfterLLM(_ context.Context, resp *LLMHookResponse) (*LLMHookResponse, HookDecision, error) {
	return resp, HookDecision{Action: HookActionContinue}, nil
}

func (h *routingModelHook) BeforeLLM(_ context.Context, req *LLMHookRequest) (*LLMHookRequest, HookDecision, error) {
	h.calls++
	if h.calls == 1 {
		req = req.Clone()
		req.Model = "hook-model"
	}
	return req, HookDecision{Action: HookActionModify}, nil
}

func TestModelHookOverridesSelectedCandidateAndKeepsNativeStickyReserve(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprint(inline), func(t *testing.T) {
			var calls []string
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Model string `json:"model"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				calls = append(calls, r.Header.Get("Authorization")+":"+input.Model)
				if input.Model == "hook-model" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
					return
				}
				if len(calls) == 2 {
					_, _ = w.Write(
						[]byte(
							`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"once","type":"function","function":{"name":"mock_custom","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
						),
					)
					return
				}
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"reserve answer"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer endpoint.Close()
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{
					ID:        "primary",
					ModelName: "primary",
					Model:     "openai/primary",
					APIBase:   endpoint.URL,
					APIKey:    "primary-key",
				},
				{
					ID:        "reserve",
					ModelName: "reserve",
					Model:     "openai/reserve",
					APIBase:   endpoint.URL,
					APIKey:    "reserve-key",
				},
			}, []string{"reserve"})
			al.RegisterTool(&mockCustomTool{})
			require.NoError(t, al.MountHook(NamedHook("model-override", &routingModelHook{})))
			msg := directIngressMessage("use a tool")
			if inline {
				msg = selectedInlineMessage("42", "hook-routing", "use a tool")
			}
			answer, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Contains(t, answer, "reserve answer")
			require.Equal(
				t,
				[]string{"Bearer primary-key:hook-model", "Bearer reserve-key:reserve", "Bearer reserve-key:reserve"},
				calls,
			)
		})
	}
}

func TestRemoteImageInputSkipsIncompatibleConfiguredPrimary(t *testing.T) {
	for _, media := range []string{"http://images.example/picture.png", "https://images.example/picture.png", "data:image/png;base64,YQ==", "data:audio/wav;base64,YQ=="} {
		t.Run(media, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				if media == "data:audio/wav;base64,YQ==" {
					require.Equal(t, "Bearer text-key", r.Header.Get("Authorization"))
					require.Contains(t, string(body), "input_audio")
				} else {
					require.Equal(t, "Bearer image-key", r.Header.Get("Authorization"))
					require.Contains(t, string(body), media)
				}
				_, _ = w.Write(
					[]byte(
						`{"choices":[{"message":{"role":"assistant","content":"media accepted"},"finish_reason":"stop"}]}`,
					),
				)
			}))
			defer endpoint.Close()
			noImages := false
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{
					ModelName:    "text",
					Model:        "openai/text",
					APIBase:      endpoint.URL,
					APIKey:       "text-key",
					Capabilities: &config.ModelCapabilities{Images: &noImages},
				},
				{ModelName: "image", Model: "openai/image", APIBase: endpoint.URL, APIKey: "image-key"},
			}, []string{"image"})
			answer, err := al.runAgentLoop(t.Context(), al.registry.GetDefaultAgent(), processOptions{
				SessionKey: "media", Channel: "cli", ChatID: "media", UserMessage: "describe", Media: []string{media},
			})
			require.NoError(t, err)
			require.Equal(t, "media accepted", answer)
			require.Equal(t, 1, calls)
		})
	}
}
