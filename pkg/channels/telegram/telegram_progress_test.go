package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestToolActivityUpdatesEachInvocationIndependentlyOfTheAnswer(t *testing.T) {
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
				channel.Send(context.Background(), bus.OutboundMessage{ChatID: chat, Content: "Final answer $x^2$"}),
			)
			require.Equal(t, 5, created, "each tool and the final answer must have their own message")
			require.Len(t, caller.calls, 9, "repeated completion delivery must not duplicate results")
			for i := 0; i < 4; i++ {
				var edited struct {
					MessageID int             `json:"message_id"`
					Rich      json.RawMessage `json:"rich_message"`
				}
				require.NoError(t, json.Unmarshal(caller.calls[i*2+1].Data.BodyRaw, &edited))
				require.Equal(t, i+1, edited.MessageID)
				if i == 0 {
					require.Contains(t, string(edited.Rich), `"type":"details"`)
					require.Contains(t, string(edited.Rich), "truncated")
				}
				if i == 3 {
					require.Contains(t, string(edited.Rich), "permission denied")
				}
			}
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
	message.Progress.State = bus.ProgressCompleted
	message.Progress.Result = "done"
	require.Error(t, channel.SendProgress(context.Background(), message))
	require.NoError(t, channel.SendProgress(context.Background(), message))
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: "Final answer"}))
	require.Len(t, caller.calls, 4)
	require.Contains(t, caller.calls[1].URL, "/editMessageText")
	require.Contains(t, caller.calls[2].URL, "/editMessageText")
	require.Contains(t, caller.calls[3].URL, "/sendRichMessage")
}
