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

func TestRichReplyNormalizesMathOutsideActualCodeFences(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"prose tildes", `literal ~~~ then \(x\)`, `literal ~~~ then $x$`},
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
	channel.BaseChannel = channels.NewBaseChannel("telegram", nil, bus.NewMessageBus(), nil)
	channel.SetRunning(true)
	require.NoError(
		t,
		channel.handleChosenInlineResult((&th.Context{}).WithContext(context.Background()), telego.ChosenInlineResult{
			ResultID: "generate", InlineMessageID: "published-result", Query: "long answer", From: telego.User{ID: 42},
		}),
	)
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
