package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

func TestRichRepliesPreserveFormattingFormulasAndTopicReply(t *testing.T) {
	const content = "# Results\n\n| Value | Formula |\n| --- | --- |\n| one | \\(x^2\\) |\n\n- item\n\n```go\nfmt.Println(\"ok\")\n```\n\n\\[E = mc^2\\]"
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "-123/9", ReplyToMessageID: "42", Content: content,
	}))
	require.Len(t, caller.calls, 1)
	require.Contains(t, caller.calls[0].URL, "/sendRichMessage")
	var payload struct {
		ChatID   int64 `json:"chat_id"`
		ThreadID int   `json:"message_thread_id"`
		Reply    struct {
			MessageID int `json:"message_id"`
		} `json:"reply_parameters"`
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[0].Data.BodyRaw, &payload))
	require.Equal(t, int64(-123), payload.ChatID)
	require.Equal(t, 9, payload.ThreadID)
	require.Equal(t, 42, payload.Reply.MessageID)
	require.Equal(
		t,
		"# Results\n\n| Value | Formula |\n| --- | --- |\n| one | $x^2$ |\n\n- item\n\n```go\nfmt.Println(\"ok\")\n```\n\n$$E = mc^2$$",
		payload.Rich.Markdown,
	)
}

func TestIndependentIdenticalRichMessagesDoNotShareFailedDelivery(t *testing.T) {
	calls := 0
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("unknown send outcome")
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	msg := bus.OutboundMessage{DeliveryID: "operation-a", ChatID: "42", Content: "identical notification"}
	require.ErrorIs(t, channel.Send(t.Context(), msg), channels.ErrSendFailed)
	require.ErrorIs(t, channel.Send(t.Context(), msg), channels.ErrSendFailed)
	require.Equal(t, 1, calls, "an uncertain send must not be replayed for the same operation")
	msg.DeliveryID = ""
	require.NoError(t, channel.Send(t.Context(), msg))
	require.Equal(t, 2, calls, "independent messages without a reply reference need separate delivery identities")
}

func TestRichReplyNormalizesMathOutsideActualCodeFences(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"prose tildes", `literal ~~~ then \(x\)`, `literal ~~~ then $x$`},
		{"invalid backtick fence info", "```not`fence\n\\(x\\)", "```not`fence\n$x$"},
		{"indented prose", "    ~~~\n\\(x\\)", "    ~~~\n$x$"},
		{"longer closing fence", "   ~~~latex\n\\(code\\)\n ~~~~~\n\\(x\\)", "   ~~~latex\n\\(code\\)\n ~~~~~\n$x$"},
		{"invalid closing fence", "~~~\n\\(code\\)\ntext ~~~\n\\(still code\\)", "~~~\n\\(code\\)\ntext ~~~\n\\(still code\\)"},
		{"short closing fence", "````\n\\(code\\)\n```\n\\(still code\\)", "````\n\\(code\\)\n```\n\\(still code\\)"},
		{"inline code", "`\\(code\\)` then \\(x\\)", "`\\(code\\)` then $x$"},
		{"unfinished real fence", "before \\(x\\)\n~~~\n\\(code\\)", "before $x$\n~~~\n\\(code\\)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) { return successResponse(t), nil }}
			channel := newTestChannel(t, caller)
			require.NoError(t, channel.Send(t.Context(), bus.OutboundMessage{ChatID: "42", Content: tc.source}))
			var payload struct {
				Rich struct {
					Markdown string `json:"markdown"`
				} `json:"rich_message"`
			}
			require.NoError(t, json.Unmarshal(caller.calls[0].Data.BodyRaw, &payload))
			require.Equal(t, tc.want, payload.Rich.Markdown)
		})
	}
}

func TestInboundPlaceholderRepliesToOriginalMessageInTopic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Placeholder.Enabled = true
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	manager, err := channels.NewManager(channel.config, mb, nil)
	require.NoError(t, err)
	channel.BaseChannel.SetOwner(channel)
	channel.BaseChannel.SetPlaceholderRecorder(manager)
	channel.HandleMessage(ctx, bus.Peer{Kind: "group", ID: "-123"}, "42", "7", "-123/9", "question", nil, nil)
	for _, call := range caller.calls {
		if !strings.HasSuffix(call.URL, "/sendMessage") {
			continue
		}
		var payload struct {
			Thread int `json:"message_thread_id"`
			Reply  struct {
				MessageID int `json:"message_id"`
			} `json:"reply_parameters"`
		}
		require.NoError(t, json.Unmarshal(call.Data.BodyRaw, &payload))
		require.Equal(t, 9, payload.Thread)
		require.Equal(t, 42, payload.Reply.MessageID)
		return
	}
	t.Fatal("inbound request did not create a placeholder")
}

func TestInlineRichReplyKeepsLongAnswersAvailableAfterRestart(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	channel := newTestChannel(t, caller)
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.config.Channels.Telegram.Inline.Enabled = true
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	channel.SetRunning(true)
	require.NoError(
		t,
		channel.handleChosenInlineResult((&th.Context{}).WithContext(context.Background()), telego.ChosenInlineResult{
			ResultID: "generate", InlineMessageID: "published-result", Query: "long answer", From: telego.User{ID: 42},
		}),
	)
	<-mb.InboundChan()
	caller.calls = nil
	content := strings.Repeat("first paragraph\n\n", 2300) + "LAST ANSWER LINE"
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "inline:published-result", Content: content,
	}))
	var first struct {
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
		Markup struct {
			Buttons [][]struct {
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[0].Data.BodyRaw, &first))
	require.Contains(t, first.Rich.Markdown, "first paragraph")
	require.LessOrEqual(t, len([]rune(first.Rich.Markdown)), 32768)
	require.NotEmpty(t, first.Markup.Buttons)

	reopened := newTestChannel(t, caller)
	reopened.config = channel.config
	reopened.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	for _, current := range []*TelegramChannel{channel, reopened} {
		before := len(caller.calls)
		require.NoError(t, current.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), telego.ChosenInlineResult{
			ResultID: "generate", InlineMessageID: "published-result", Query: "long answer", From: telego.User{ID: 42},
		}))
		require.Len(t, caller.calls, before, "chosen-result redelivery must retain the published keyboard")
		select {
		case duplicate := <-mb.InboundChan():
			t.Fatalf("chosen-result redelivery dispatched generation again: %+v", duplicate)
		default:
		}
	}
	require.NoError(
		t,
		reopened.handleInlineCallbackQuery(
			(&th.Context{}).WithContext(context.Background()),
			telego.CallbackQuery{
				ID:              "callback",
				InlineMessageID: "published-result",
				Data:            first.Markup.Buttons[0][0].Data,
				From:            telego.User{ID: 7},
			},
		),
	)
	require.Contains(t, caller.calls[len(caller.calls)-1].URL, "/answerCallbackQuery")
	require.Len(t, caller.calls, 2, "another user's callback must not edit the published result")
	require.NoError(
		t,
		reopened.handleInlineCallbackQuery(
			(&th.Context{}).WithContext(context.Background()),
			telego.CallbackQuery{
				ID:              "callback",
				InlineMessageID: "published-result",
				Data:            first.Markup.Buttons[0][0].Data,
				From:            telego.User{ID: 42},
			},
		),
	)
	var last struct {
		Rich struct {
			Markdown string `json:"markdown"`
		} `json:"rich_message"`
	}
	require.NoError(t, json.Unmarshal(caller.calls[len(caller.calls)-2].Data.BodyRaw, &last))
	require.Contains(t, last.Rich.Markdown, "LAST ANSWER LINE")
}

func TestChosenInlineFailedAdmissionCanRetryAfterRestartAndRetainsShortCompletion(t *testing.T) {
	caller := &stubCaller{callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel := newTestChannel(t, caller)
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.config.Channels.Telegram.Inline.Enabled = true
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	result := telego.ChosenInlineResult{ResultID: "generate", InlineMessageID: "retry-publication", Query: "question", From: telego.User{ID: 42}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, channel.handleChosenInlineResult((&th.Context{}).WithContext(ctx), result), context.Canceled)
	reopened := newTestChannel(t, caller)
	reopened.config = channel.config
	reopened.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	require.NoError(t, reopened.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	select {
	case <-mb.InboundChan():
	case <-time.After(time.Second):
		t.Fatal("failed chosen admission could not recover after restart")
	}
	require.NoError(t, reopened.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	select {
	case <-mb.InboundChan():
		t.Fatal("active chosen request was admitted twice")
	default:
	}
	reopened.SetRunning(true)
	require.NoError(t, reopened.Send(t.Context(), bus.OutboundMessage{ChatID: "inline:retry-publication", Content: "short completed answer"}))
	third := newTestChannel(t, caller)
	third.config = channel.config
	third.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	require.NoError(t, third.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	select {
	case <-mb.InboundChan():
		t.Fatal("short completed publication was regenerated after restart")
	default:
	}
}

func TestChosenInlineRetainedSourceRecoversFailedPublicationWithoutGeneration(t *testing.T) {
	failEdit := false
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if failEdit && strings.HasSuffix(url, "/editMessageText") {
			return &ta.Response{Ok: false, Error: &ta.Error{ErrorCode: 403, Description: "edit rejected"}}, nil
		}
		return &ta.Response{Ok: true, Result: json.RawMessage(`true`)}, nil
	}}
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel := newTestChannel(t, caller)
	channel.config.Agents.Defaults.Workspace = t.TempDir()
	channel.config.Channels.Telegram.Inline.Enabled = true
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	channel.SetRunning(true)
	result := telego.ChosenInlineResult{ResultID: "generate", InlineMessageID: "retained-publication", Query: "question", From: telego.User{ID: 42}}
	require.NoError(t, channel.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	<-mb.InboundChan()
	failEdit = true
	require.Error(t, channel.Send(t.Context(), bus.OutboundMessage{ChatID: "inline:retained-publication", Content: "RETAINED_COMPLETE_ANSWER"}))
	failEdit = false
	reopened := newTestChannel(t, caller)
	reopened.config = channel.config
	reopened.BaseChannel = channels.NewBaseChannel("telegram", nil, mb, nil)
	before := len(caller.calls)
	require.NoError(t, reopened.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	require.Len(t, caller.calls, before+1, "retained generated text must recover through one stable edit")
	require.Contains(t, string(caller.calls[len(caller.calls)-1].Data.BodyRaw), "RETAINED_COMPLETE_ANSWER")
	select {
	case <-mb.InboundChan():
		t.Fatal("retained answer caused another model generation")
	default:
	}
	require.NoError(t, reopened.handleChosenInlineResult((&th.Context{}).WithContext(t.Context()), result))
	require.Len(t, caller.calls, before+1, "confirmed publication must remain untouched")
}

func TestRichReplyFormattingFailureDeliversAllPlainContent(t *testing.T) {
	caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
		if strings.HasSuffix(url, "/sendRichMessage") {
			return &ta.Response{
				Ok:    false,
				Error: &ta.Error{ErrorCode: 400, Description: "Bad Request: can't parse rich message"},
			}, nil
		}
		return successResponse(t), nil
	}}
	channel := newTestChannel(t, caller)
	content := strings.Repeat("a", 5000) + "END"
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: content}))
	var received strings.Builder
	for _, call := range caller.calls {
		if !strings.HasSuffix(call.URL, "/sendMessage") {
			continue
		}
		var payload struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		require.NoError(t, json.Unmarshal(call.Data.BodyRaw, &payload))
		require.Empty(t, payload.ParseMode)
		require.LessOrEqual(t, len([]rune(payload.Text)), 4096)
		received.WriteString(payload.Text)
	}
	require.Equal(t, content, received.String())
}

func TestRichReplyLongContentAndCodeExamplesRemainComplete(t *testing.T) {
	caller := &stubCaller{
		callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) { return successResponse(t), nil },
	}
	channel := newTestChannel(t, caller)
	content := "```latex\n\\(do not change\\)\n```\n\n" + strings.Repeat("a", 33000) + "END"
	require.NoError(t, channel.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: content}))
	require.Len(t, caller.calls, 2)
	var received strings.Builder
	for _, call := range caller.calls {
		var payload struct {
			Rich struct {
				Markdown string `json:"markdown"`
			} `json:"rich_message"`
		}
		require.NoError(t, json.Unmarshal(call.Data.BodyRaw, &payload))
		require.LessOrEqual(t, len([]rune(payload.Rich.Markdown)), 32768)
		received.WriteString(payload.Rich.Markdown)
	}
	require.Contains(t, received.String(), "\\(do not change\\)")
	require.Contains(t, received.String(), strings.Repeat("a", 33000)+"END")
}
