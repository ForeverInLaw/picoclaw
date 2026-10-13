package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

func TestInlineRetryStatusNeverChangesAnswerAndClearsOnRecovery(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	channel.config.Channels.Telegram.Streaming.ThrottleSeconds = 0
	stream, err := channel.BeginStream(context.Background(), "inline:result")
	require.NoError(t, err)
	require.NoError(t, stream.Update(context.Background(), "Partial answer $x^2$"))
	progress := bus.OutboundMessage{
		ChatID:  "inline:result",
		Content: "Temporary failure. Retrying (1/2)…",
		Progress: &bus.ProgressMessage{
			RequestID: "request",
			ActionID:  "model:1",
			Kind:      bus.ProgressRetry,
			State:     bus.ProgressStarted,
		},
	}
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	require.Len(t, caller.calls, 2)
	require.Contains(t, caller.calls[1].URL, "/editMessageReplyMarkup")
	var status struct {
		InlineID string                       `json:"inline_message_id"`
		Text     string                       `json:"text"`
		Rich     json.RawMessage              `json:"rich_message"`
		Markup   *telego.InlineKeyboardMarkup `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[1].Data.BodyRaw, &status))
	require.Equal(t, "result", status.InlineID)
	require.Empty(t, status.Text)
	require.Empty(t, status.Rich)
	require.NotNil(t, status.Markup.InlineKeyboard[0][0].Disabled)
	require.Contains(t, status.Markup.InlineKeyboard[0][0].Text, "Retrying")
	require.NoError(t, stream.Update(context.Background(), "Resumed answer $x^2$"))
	var preview struct {
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
		Markup *telego.InlineKeyboardMarkup `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[2].Data.BodyRaw, &preview))
	require.Equal(t, "Resumed answer $x^2$", preview.Rich.Markdown)
	require.NotNil(t, preview.Markup.InlineKeyboard[0][0].Disabled)
	progress.Progress.State = bus.ProgressCompleted
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	var recovered struct {
		Markup *telego.InlineKeyboardMarkup `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[3].Data.BodyRaw, &recovered))
	require.Empty(t, recovered.Markup.InlineKeyboard)
	require.NoError(t, stream.Finalize(context.Background(), "Complete answer $x^2$"))
	progress.Progress.State = bus.ProgressStarted
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	require.Len(t, caller.calls, 5, "late queued status must not reappear after the final answer")
}

func TestInlineRetryStatusFailureLeavesFinalErrorDeliveryAvailable(t *testing.T) {
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageReplyMarkup") {
			return &ta.Response{
				Ok:    false,
				Error: &ta.Error{ErrorCode: 500, Description: "temporary status edit failure"},
			}, nil
		}
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	stream, err := channel.BeginStream(t.Context(), "inline:result")
	require.NoError(t, err)
	require.NoError(t, stream.Update(t.Context(), "Partial answer"))
	caller.calls = nil
	progress := bus.OutboundMessage{
		ChatID:  "inline:result",
		Content: "Retrying…",
		Progress: &bus.ProgressMessage{
			RequestID: "request",
			ActionID:  "model:1",
			Kind:      bus.ProgressRetry,
			State:     bus.ProgressStarted,
		},
	}
	require.Error(t, channel.SendProgress(context.Background(), progress))
	require.NoError(
		t,
		channel.Send(
			context.Background(),
			bus.OutboundMessage{ChatID: "inline:result", Content: "The model request failed. Please try again."},
		),
	)
	progress.Progress.State = bus.ProgressFailed
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	require.Len(t, caller.calls, 2)
	var final struct {
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
		Markup *telego.InlineKeyboardMarkup `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[1].Data.BodyRaw, &final))
	require.Contains(t, final.Rich.Markdown, "request failed")
	require.Empty(t, final.Markup.InlineKeyboard)
}

func TestInlineRetryClearsWithoutReplacingFinalPageNavigation(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Inline.Enabled = true
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, bus.NewMessageBus(), nil)
	channel.SetRunning(true)
	require.NoError(
		t,
		channel.handleChosenInlineResult(
			(&th.Context{}).WithContext(context.Background()),
			telego.ChosenInlineResult{
				InlineMessageID: "result",
				ResultID:        "generate",
				Query:           "long answer",
				From:            telego.User{ID: 42},
			},
		),
	)
	caller.calls = nil
	progress := bus.OutboundMessage{
		ChatID:  "inline:result",
		Content: "Retrying (1/2)…",
		Progress: &bus.ProgressMessage{
			RequestID: "request",
			ActionID:  "model:1",
			Kind:      bus.ProgressRetry,
			State:     bus.ProgressStarted,
		},
	}
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	require.NoError(
		t,
		channel.Send(
			context.Background(),
			bus.OutboundMessage{ChatID: "inline:result", Content: strings.Repeat("Answer paragraph\n\n", 2000)},
		),
	)
	progress.Progress.State = bus.ProgressCompleted
	require.NoError(t, channel.SendProgress(context.Background(), progress))
	require.Len(t, caller.calls, 2)
	var final struct {
		Markup *telego.InlineKeyboardMarkup `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[1].Data.BodyRaw, &final))
	require.NotEmpty(t, final.Markup.InlineKeyboard)
	require.Equal(t, "Next →", final.Markup.InlineKeyboard[0][0].Text)
	require.Nil(t, final.Markup.InlineKeyboard[0][0].Disabled)
	require.Contains(t, final.Markup.InlineKeyboard[0][0].CallbackData, telegramInlinePagePrefix)
}

func TestInlineRetryIgnoresFinalizedAnswersAfterGuardEviction(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Inline.Enabled = true
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, bus.NewMessageBus(), nil)
	channel.SetRunning(true)
	require.NoError(
		t,
		channel.handleChosenInlineResult(
			(&th.Context{}).WithContext(t.Context()),
			telego.ChosenInlineResult{
				InlineMessageID: "selected",
				ResultID:        "generate",
				Query:           "answer",
				From:            telego.User{ID: 42},
			},
		),
	)
	progress := bus.OutboundMessage{
		ChatID:  "inline:selected",
		Content: "Retrying…",
		Progress: &bus.ProgressMessage{
			RequestID: "request",
			ActionID:  "retry",
			Kind:      bus.ProgressRetry,
			State:     bus.ProgressStarted,
		},
	}
	require.NoError(t, channel.SendProgress(t.Context(), progress))
	require.Len(t, caller.calls, 2, "selected requests must show retry status before their first preview")
	require.NoError(
		t,
		channel.Send(t.Context(), bus.OutboundMessage{ChatID: "inline:selected", Content: "Completed answer"}),
	)
	for n := 0; n < 2049; n++ {
		require.NoError(
			t,
			channel.Send(
				t.Context(),
				bus.OutboundMessage{ChatID: fmt.Sprintf("inline:other-%d", n), Content: "Completed answer"},
			),
		)
	}
	caller.calls = nil
	require.NoError(t, channel.SendProgress(t.Context(), progress))
	require.Empty(t, caller.calls, "an evicted final guard must not recreate an active result and replace its keyboard")
}
