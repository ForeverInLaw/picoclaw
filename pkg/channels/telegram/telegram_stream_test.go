package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

func TestRichDraftPersistsOneFinalReplyAfterPreviewFailureAndProgress(t *testing.T) {
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/sendRichMessageDraft") {
			return &ta.Response{Ok: false, Error: &ta.Error{ErrorCode: 500, Description: "preview unavailable"}}, nil
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	channel.config.Channels.Telegram.Streaming.ThrottleSeconds = 0
	ctx := bus.WithStreamReplyTo(context.Background(), "42")
	stream, err := channel.BeginStream(ctx, "123/7")
	require.NoError(t, err)
	require.NoError(t, stream.Update(ctx, "Answer \\(x^2\\)"))
	require.NoError(
		t,
		channel.SendProgress(
			ctx,
			bus.OutboundMessage{
				ChatID:  "123/7",
				Content: "Retrying…",
				Progress: &bus.ProgressMessage{
					RequestID: "request",
					ActionID:  "retry",
					Kind:      bus.ProgressRetry,
					State:     bus.ProgressStarted,
				},
			},
		),
	)
	require.NoError(t, stream.Finalize(ctx, "# Answer\n\n\\[E = mc^2\\]"))
	require.NoError(t, stream.Finalize(ctx, "# Answer\n\n\\[E = mc^2\\]"))
	require.Len(t, caller.calls, 3, "one failed draft, separate progress, one persisted final")
	require.Contains(t, caller.calls[0].URL, "/sendRichMessageDraft")
	var final struct {
		Thread int `json:"message_thread_id"`
		Reply  struct {
			ID int `json:"message_id"`
		} `json:"reply_parameters"`
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[2].Data.BodyRaw, &final))
	require.Equal(t, 7, final.Thread)
	require.Equal(t, 42, final.Reply.ID)
	require.Equal(t, "# Answer\n\n$$E = mc^2$$", final.Rich.Markdown)
}

func TestInlinePreviewFailureStillDeliversCompleteRichFinalOnce(t *testing.T) {
	edits := 0
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageText") {
			edits++
			if edits == 1 {
				return &ta.Response{
					Ok:    false,
					Error: &ta.Error{ErrorCode: 500, Description: "preview unavailable"},
				}, nil
			}
		}
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.config.Channels.Telegram.Inline.Enabled = true
	channel.config.Channels.Telegram.Streaming.Enabled = true
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, bus.NewMessageBus(), nil)
	channel.SetRunning(true)
	require.NoError(
		t,
		channel.handleChosenInlineResult(
			(&th.Context{}).WithContext(context.Background()),
			telego.ChosenInlineResult{
				InlineMessageID: "result",
				ResultID:        "generate",
				Query:           "answer",
				From:            telego.User{ID: 42},
			},
		),
	)
	caller.calls = nil
	stream, err := channel.BeginStream(context.Background(), "inline:result")
	require.NoError(t, err)
	require.NoError(t, stream.Update(context.Background(), "Preview"))
	content := strings.Repeat("answer ", 1000) + "$$E = mc^2$$"
	require.NoError(t, stream.Finalize(context.Background(), content))
	require.NoError(t, stream.Finalize(context.Background(), content))
	require.Len(t, caller.calls, 2)
	var final struct {
		ID   string `json:"inline_message_id"`
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[1].Data.BodyRaw, &final))
	require.Equal(t, "result", final.ID)
	require.Equal(t, content, final.Rich.Markdown)
}

func TestCancelledStreamDoesNotPublishAnAnswer(t *testing.T) {
	caller := &stubCaller{
		callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) { return successResponse(t), nil },
	}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	stream, err := channel.BeginStream(context.Background(), "123")
	require.NoError(t, err)
	stream.Cancel(context.Background())
	require.NoError(t, stream.Update(context.Background(), "preview"))
	require.ErrorIs(t, stream.Finalize(context.Background(), "answer"), context.Canceled)
	require.Empty(t, caller.calls)
}

func TestGroupRichStreamUsesMessageEditsAndKeepsFinalAfterEditFailure(t *testing.T) {
	edits := 0
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/editMessageText") {
			edits++
			if edits == 1 {
				return &ta.Response{
					Ok:    false,
					Error: &ta.Error{ErrorCode: 500, Description: "preview update failed"},
				}, nil
			}
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	channel.config.Channels.Telegram.Streaming.ThrottleSeconds = 0
	ctx := bus.WithStreamReplyTo(context.Background(), "42")
	stream, err := channel.BeginStream(ctx, "-123/7")
	require.NoError(t, err)
	require.NoError(t, stream.Update(ctx, "First answer preview"))
	require.NoError(t, stream.Update(ctx, "Longer answer preview"))
	require.NoError(t, stream.Finalize(ctx, "Complete answer $x^2$"))
	require.NoError(t, stream.Finalize(ctx, "Complete answer $x^2$"))
	require.Len(t, caller.calls, 3)
	require.Contains(t, caller.calls[0].URL, "/sendRichMessage")
	require.Contains(t, caller.calls[1].URL, "/editMessageText")
	require.Contains(t, caller.calls[2].URL, "/editMessageText")
	for _, call := range caller.calls {
		require.NotContains(t, call.URL, "Draft")
	}
}
