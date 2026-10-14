package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestToolActivityGroupsQuietStartsSeparatelyFromTheAnswer(t *testing.T) {
	for _, chat := range []string{"123", "-123/7"} {
		t.Run(chat, func(t *testing.T) {
			created := 0
			caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
				if strings.HasSuffix(url, "/sendRichMessage") {
					created++
				}
				data, err := json.Marshal(telego.Message{MessageID: created})
				require.NoError(t, err)
				return &ta.Response{Ok: true, Result: data}, nil
			}}
			channel := newTestChannel(t, caller)
			t.Cleanup(func() { _ = channel.Stop(context.Background()) })
			for i, tool := range []string{"exec", "read_file", "web_search", "custom_tool"} {
				message := bus.OutboundMessage{ChatID: chat, ReplyToMessageID: "42", Progress: &bus.ProgressMessage{
					RequestID: "request", ActionID: fmt.Sprintf("call-%d", i), Kind: bus.ProgressTool,
					State: bus.ProgressStarted, Tool: tool, Arguments: map[string]any{"command": "ls /workspace"},
				}}
				require.NoError(t, channel.SendProgress(context.Background(), message))
				message.Progress.State = bus.ProgressCompleted
				message.Progress.Result = "Done: " + tool
				if i == 0 {
					message.Progress.Result = strings.Repeat("shell output\n", 3000)
				}
				if i == 3 {
					message.Progress.State = bus.ProgressFailed
					message.Progress.Error = "permission denied"
				}
				require.NoError(t, channel.SendProgress(context.Background(), message))
				require.NoError(t, channel.SendProgress(context.Background(), message))
			}
			require.NoError(
				t,
				channel.Send(
					context.Background(),
					bus.OutboundMessage{ChatID: chat, ReplyToMessageID: "42", Content: "Final answer $x^2$"},
				),
			)
			require.Equal(t, 2, created, "one tool journal and one final answer")
			require.Len(t, caller.calls, 3, "rapid starts batch into one edit before the answer")
			var first, final struct {
				DisableNotification bool                    `json:"disable_notification"`
				ReplyParameters     *telego.ReplyParameters `json:"reply_parameters"`
			}
			require.NoError(t, json.Unmarshal(caller.calls[0].Data.BodyRaw, &first))
			require.True(t, first.DisableNotification)
			require.Equal(t, 42, first.ReplyParameters.MessageID)
			require.NoError(t, json.Unmarshal(caller.calls[2].Data.BodyRaw, &final))
			require.False(t, final.DisableNotification)
			var edited struct {
				MessageID int `json:"message_id"`
			}
			require.NoError(t, json.Unmarshal(caller.calls[1].Data.BodyRaw, &edited))
			require.Equal(t, 1, edited.MessageID)
			body := string(caller.calls[1].Data.BodyRaw)
			require.Contains(t, body, "Searching the web for")
			require.Contains(t, body, "Reading")
			require.Contains(t, body, "custom_tool")
			require.NotContains(t, body, "shell output")
			require.NotContains(t, body, "permission denied")
		})
	}
}

func TestToolActivityEditRetryKeepsOneMessageAndFinalAnswer(t *testing.T) {
	edits := 0
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageText") {
			edits++
			if edits == 1 {
				return &ta.Response{
					Ok:    false,
					Error: &ta.Error{ErrorCode: 500, Description: "temporary Telegram failure"},
				}, nil
			}
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	message := bus.OutboundMessage{
		ChatID: "123",
		Progress: &bus.ProgressMessage{
			RequestID: "request",
			ActionID:  "call",
			Kind:      bus.ProgressTool,
			State:     bus.ProgressStarted,
			Tool:      "exec",
			Arguments: map[string]any{"command": "echo done"},
		},
	}
	require.NoError(t, channel.SendProgress(context.Background(), message))
	message.Progress.ActionID = "second-call"
	require.NoError(t, channel.SendProgress(context.Background(), message))
	func() {
		channel.progressMu.Lock()
		defer channel.progressMu.Unlock()
		entry := channel.activities["123:request:tool"]
		require.Error(t, channel.flushActivity(context.Background(), entry))
		require.NoError(t, channel.flushActivity(context.Background(), entry))
	}()
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: "Final answer"}))
	require.Len(t, caller.calls, 4)
	require.Contains(t, caller.calls[1].URL, "/editMessageText")
	require.Contains(t, caller.calls[2].URL, "/editMessageText")
	require.Contains(t, caller.calls[3].URL, "/sendRichMessage")
}

func TestToolActivityTimerGroupsRepeatsWithoutWaitingForCompletion(t *testing.T) {
	updated := make(chan string, 1)
	caller := &stubCaller{callFn: func(_ context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageText") {
			updated <- string(data.BodyRaw)
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	msg := bus.OutboundMessage{ChatID: "123", Progress: &bus.ProgressMessage{
		RequestID: "request", ActionID: "first", Kind: bus.ProgressTool, State: bus.ProgressStarted,
		Tool: "exec", Arguments: map[string]any{"command": "printf hello\nprintf private-tail"},
	}}
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.NoError(t, channel.SendProgress(t.Context(), msg), "delivery retry is not a second invocation")
	msg.Progress.ActionID = "second"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	select {
	case body := <-updated:
		require.Contains(t, body, "printf hello ... (×2)")
		require.NotContains(t, body, "private-tail")
		require.Contains(t, body, `"type":"pre"`)
	case <-time.After(5 * time.Second):
		t.Fatal("accepted starts were not flushed during a running tool")
	}
	require.NoError(t, channel.Stop(t.Context()))
	require.Len(t, caller.calls, 2, "one quiet send, one coalesced edit")
}

func TestToolActivityFallbackAndDeletedMessageStayQuiet(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
				if fallback && strings.HasSuffix(url, "/sendRichMessage") {
					return &ta.Response{
						Ok:    false,
						Error: &ta.Error{ErrorCode: 400, Description: "invalid rich message"},
					}, nil
				}
				if strings.HasSuffix(url, "/editMessageText") {
					return &ta.Response{
						Ok:    false,
						Error: &ta.Error{ErrorCode: 400, Description: "message to edit not found"},
					}, nil
				}
				return successResponse(t), nil
			}}
			channel := newTestChannel(t, caller)
			t.Cleanup(func() { _ = channel.Stop(context.Background()) })
			msg := bus.OutboundMessage{ChatID: "-123/7", ReplyToMessageID: "42", Progress: &bus.ProgressMessage{
				RequestID: "request", ActionID: "first", Kind: bus.ProgressTool, State: bus.ProgressStarted,
				Tool: "custom_tool", Arguments: map[string]any{"private": "not-for-display"},
			}}
			require.NoError(t, channel.SendProgress(t.Context(), msg))
			msg.Progress.ActionID = "second"
			require.NoError(t, channel.SendProgress(t.Context(), msg))
			channel.finishActivities(t.Context(), msg.ChatID, msg.ReplyToMessageID, true)
			for _, call := range caller.calls {
				if strings.Contains(call.URL, "/send") {
					var sent struct {
						Silent bool                    `json:"disable_notification"`
						Thread int                     `json:"message_thread_id"`
						Reply  *telego.ReplyParameters `json:"reply_parameters"`
					}
					require.NoError(t, json.Unmarshal(call.Data.BodyRaw, &sent))
					require.True(t, sent.Silent)
					require.Equal(t, 7, sent.Thread)
					require.Equal(t, 42, sent.Reply.MessageID)
				}
				require.NotContains(t, string(call.Data.BodyRaw), "not-for-display")
			}
			require.Contains(t, string(caller.calls[len(caller.calls)-1].Data.BodyRaw), "×2")
		})
	}
}

func TestToolActivityFloodWaitDoesNotResendOrBlockTheAnswer(t *testing.T) {
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageText") {
			return &ta.Response{Ok: false, Error: &ta.Error{
				ErrorCode:   429,
				Description: "Too Many Requests",
				Parameters:  &ta.ResponseParameters{RetryAfter: 30},
			}}, nil
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	msg := bus.OutboundMessage{ChatID: "123", Progress: &bus.ProgressMessage{
		RequestID: "request", ActionID: "first", Kind: bus.ProgressTool, State: bus.ProgressStarted, Tool: "read_file",
	}}
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	msg.Progress.ActionID = "second"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	func() {
		channel.progressMu.Lock()
		defer channel.progressMu.Unlock()
		require.Error(t, channel.flushActivity(t.Context(), channel.activities["123:request:tool"]))
	}()
	msg.Progress.ActionID = "third"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.NoError(t, channel.Send(t.Context(), bus.OutboundMessage{ChatID: "123", Content: "Final answer"}))
	require.Len(t, caller.calls, 3, "flood wait must not produce extra edits or a replacement journal")
}

func TestToolActivityTextBoundaryAndFinalSeal(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	msg := bus.OutboundMessage{ChatID: "123", ReplyToMessageID: "42", Progress: &bus.ProgressMessage{
		RequestID: "request", ActionID: "first", Kind: bus.ProgressTool, State: bus.ProgressStarted, Tool: "read_file",
	}}
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.NoError(t, channel.Send(t.Context(), bus.OutboundMessage{
		ChatID: msg.ChatID, ReplyToMessageID: "42", Content: "Now checking the result",
	}))
	msg.Progress.ActionID = "second"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.NoError(t, channel.Send(t.Context(), bus.OutboundMessage{
		ChatID: msg.ChatID, ReplyToMessageID: "42", Content: "Final answer",
		DeliveryID: bus.DeliveryID(bus.EnsureDeliveryID(t.Context())),
	}))
	msg.Progress.ActionID = "late"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.Len(t, caller.calls, 4, "tools after commentary start below it; late starts never follow the final answer")
	for _, call := range caller.calls {
		require.Contains(t, call.URL, "/sendRichMessage")
	}
}

func TestToolActivityRolloverKeepsTopicsAndRequestsIndependent(t *testing.T) {
	created := 0
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/sendRichMessage") {
			created++
		}
		body, err := json.Marshal(telego.Message{MessageID: created})
		require.NoError(t, err)
		return &ta.Response{Ok: true, Result: body}, nil
	}}
	channel := newTestChannel(t, caller)
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	msg := bus.OutboundMessage{ChatID: "-123/7", ReplyToMessageID: "42", Progress: &bus.ProgressMessage{
		RequestID: "request", Kind: bus.ProgressTool, State: bus.ProgressStarted, Tool: "web_search",
	}}
	for i := range 70 {
		msg.Progress.ActionID = fmt.Sprint(i)
		msg.Progress.Arguments = map[string]any{"query": fmt.Sprintf("%03d %s", i, strings.Repeat("🙂", 50))}
		require.NoError(t, channel.SendProgress(t.Context(), msg))
	}
	channel.finishActivities(t.Context(), msg.ChatID, msg.ReplyToMessageID, true)
	require.Equal(t, 2, created, "overflow rolls to a new journal")
	msg.ChatID = "-123/8"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	msg.ChatID, msg.Progress.RequestID = "-123/7", "other-request"
	require.NoError(t, channel.SendProgress(t.Context(), msg))
	require.NoError(t, channel.Stop(t.Context()))
	require.Equal(t, 4, created, "another topic and another request need independent journals")
	for _, call := range caller.calls {
		var payload struct {
			Rich struct {
				Blocks []struct {
					Text string `json:"text"`
				} `json:"blocks"`
			} `json:"rich_message"`
		}
		require.NoError(t, json.Unmarshal(call.Data.BodyRaw, &payload))
		lines := make([]string, 0, len(payload.Rich.Blocks))
		for _, block := range payload.Rich.Blocks {
			lines = append(lines, block.Text)
		}
		require.LessOrEqual(t, len(utf16.Encode([]rune(strings.Join(lines, "\n")))), 4000)
	}
}
