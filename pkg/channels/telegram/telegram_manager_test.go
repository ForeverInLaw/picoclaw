package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// Keep the production Send/stream adapter, but do not start Telegram polling.
type outboundTelegram struct{ *TelegramChannel }

func (c outboundTelegram) Start(context.Context) error { c.SetRunning(true); return nil }
func (c outboundTelegram) Stop(context.Context) error  { c.SetRunning(false); return nil }

// A fixture clock advances the native adapter's expiry time without sleeping.
func deliveryContextAt(ctx context.Context, startedAt time.Time) context.Context {
	id := uuid.Must(uuid.NewV7())
	millis := startedAt.UnixMilli()
	for n := 5; n >= 0; n-- {
		id[n] = byte(millis)
		millis >>= 8
	}
	return bus.WithDeliveryID(ctx, id.String())
}

func TestExpiredDeliveryRemainsFailedAcrossManagerRecoveryAndStreamFinalize(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(fmt.Sprintf("unrelatedPrune=%t", unrelated), func(t *testing.T) {
			sends := 0
			caller := &stubCaller{callFn: func(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
				if strings.HasSuffix(url, "/sendRichMessage") {
					sends++
					if sends == 2 {
						return nil, errors.New("unknown delivery outcome")
					}
				}
				return successResponse(t), nil
			}}
			channel := newTestChannel(t, caller)
			channel.config.Channels.Telegram.Streaming.Enabled = true
			now := time.Now()
			channel.deliveryNow = func() time.Time { return now }
			mb := bus.NewMessageBus()
			defer mb.Close()
			manager, err := channels.NewManager(channel.config, mb, nil)
			require.NoError(t, err)
			manager.RegisterChannel("telegram", outboundTelegram{channel})
			require.NoError(t, manager.StartAll(t.Context()))
			defer func() { require.NoError(t, manager.StopAll(t.Context())) }()
			ctx := deliveryContextAt(t.Context(), now)
			stream, ok := manager.GetStreamer(ctx, "telegram", "42")
			require.True(t, ok)
			content := strings.Repeat("answer paragraph\n", 3000)
			require.ErrorIs(t, stream.Finalize(ctx, content), channels.ErrSendFailed)
			now = now.Add(11 * time.Minute)
			wantSends := 2
			if unrelated {
				freshCtx := deliveryContextAt(t.Context(), now)
				require.NoError(
					t,
					manager.SendMessage(
						freshCtx,
						bus.OutboundMessage{Channel: "telegram", ChatID: "42", Content: "independent B"},
					),
				)
				wantSends++
			}
			msg := bus.OutboundMessage{
				DeliveryID: bus.DeliveryID(ctx),
				Channel:    "telegram",
				ChatID:     "42",
				Content:    content,
			}
			require.ErrorIs(t, manager.SendMessage(ctx, msg), channels.ErrSendFailed)
			require.ErrorIs(
				t,
				stream.Finalize(ctx, content),
				channels.ErrSendFailed,
				"expiry cannot falsely confirm a failed stream",
			)
			require.Equal(t, wantSends, sends, "pruning must not authorize retransmission of the original operation")
		})
	}
}

func TestBeginStreamActivatesIdentityOnlyAtFirstFinalDelivery(t *testing.T) {
	caller := &stubCaller{
		callFn: func(context.Context, string, *ta.RequestData) (*ta.Response, error) { return successResponse(t), nil },
	}
	channel := newTestChannel(t, caller)
	channel.config.Channels.Telegram.Streaming.Enabled = true
	ctx := bus.EnsureDeliveryID(t.Context())
	stream, err := channel.BeginStream(ctx, "42")
	require.NoError(t, err)
	require.NoError(t, stream.Update(t.Context(), "preview"))
	time.Sleep(20 * time.Millisecond)
	beforeFinal := time.Now().Truncate(time.Millisecond)
	require.NoError(t, stream.Finalize(t.Context(), "final answer"))
	started, err := bus.DeliveryStartedAt(bus.DeliveryID(ctx))
	require.NoError(t, err)
	require.False(
		t,
		started.Before(beforeFinal),
		"provider/tool generation and previews must not consume the final recovery lifetime",
	)
}

func TestManagerPreservesCompleteInlineAnswerForNavigation(t *testing.T) {
	for _, asynchronous := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%t", asynchronous), func(t *testing.T) {
			edits := make(chan json.RawMessage, 16)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/editMessageText") {
					var payload json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					edits <- payload
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			}))
			defer server.Close()
			bot, err := telego.NewBot(testToken, telego.WithAPIServer(server.URL),
				telego.WithHTTPClient(server.Client()), telego.WithDiscardLogger())
			require.NoError(t, err)
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			mb := bus.NewMessageBus()
			defer mb.Close()
			manager, err := channels.NewManager(cfg, mb, nil)
			require.NoError(t, err)
			cfg.Channels.Telegram.Inline.Enabled = true
			channel := &TelegramChannel{bot: bot, config: cfg, BaseChannel: channels.NewBaseChannel(
				"telegram", nil, mb, nil, channels.WithMaxMessageLength(telegramRichMessageLimit),
			)}
			manager.RegisterChannel("telegram", outboundTelegram{channel})
			require.NoError(t, manager.StartAll(context.Background()))
			defer func() { require.NoError(t, manager.StopAll(context.Background())) }()
			require.NoError(t, channel.handleChosenInlineResult((&th.Context{}).WithContext(context.Background()),
				telego.ChosenInlineResult{
					ResultID: "generate", InlineMessageID: "published", Query: "answer",
					From: telego.User{ID: 42},
				}))
			content := "FIRST ANSWER LINE\n\n" + strings.Repeat("paragraph content\n\n", 5500) + "LAST ANSWER LINE"
			msg := bus.OutboundMessage{Channel: "telegram", ChatID: "inline:published", Content: content}
			if asynchronous {
				require.NoError(t, mb.PublishOutbound(context.Background(), msg))
			} else {
				require.NoError(t, manager.SendMessage(context.Background(), msg))
			}
			readEdit := func() string {
				t.Helper()
				select {
				case payload := <-edits:
					var body struct {
						Rich struct {
							Markdown string `json:"markdown"`
						} `json:"rich_message"`
					}
					require.NoError(t, json.Unmarshal(payload, &body))
					return body.Rich.Markdown
				case <-time.After(2 * time.Second):
					t.Fatal("missing Telegram answer edit")
					return ""
				}
			}
			first := readEdit()
			require.Contains(t, first, "FIRST ANSWER LINE")
			require.True(
				t,
				strings.Contains(first, "Page 1/4"),
				"manager must pass the complete answer to Telegram pagination",
			)
			require.NoError(t, channel.handleInlineCallbackQuery((&th.Context{}).WithContext(context.Background()),
				telego.CallbackQuery{
					ID: "navigate", InlineMessageID: "published", From: telego.User{ID: 42},
					Data: telegramInlinePagePrefix + "3",
				}))
			require.Contains(t, readEdit(), "LAST ANSWER LINE")
		})
	}
}

func TestManagerInlineFormattingFallbackPreservesLaterSourcePosition(t *testing.T) {
	var rejected, plain string
	rejectLater := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			var body struct {
				Text string `json:"text"`
				Rich struct {
					Markdown string `json:"markdown"`
				} `json:"rich_message"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if rejectLater && body.Rich.Markdown != "" {
				rejected = body.Rich.Markdown
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"too many rich message blocks"}`))
				return
			}
			if body.Text != "" {
				plain = body.Text
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	bot, err := telego.NewBot(
		testToken,
		telego.WithAPIServer(server.URL),
		telego.WithHTTPClient(server.Client()),
		telego.WithDiscardLogger(),
	)
	require.NoError(t, err)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Channels.Telegram.Inline.Enabled = true
	mb := bus.NewMessageBus()
	defer mb.Close()
	manager, err := channels.NewManager(cfg, mb, nil)
	require.NoError(t, err)
	channel := &TelegramChannel{bot: bot, config: cfg, BaseChannel: channels.NewBaseChannel("telegram", nil, mb, nil)}
	manager.RegisterChannel("telegram", outboundTelegram{channel})
	require.NoError(t, manager.StartAll(t.Context()))
	defer func() { require.NoError(t, manager.StopAll(t.Context())) }()
	require.NoError(
		t,
		channel.handleChosenInlineResult(
			(&th.Context{}).WithContext(t.Context()),
			telego.ChosenInlineResult{
				InlineMessageID: "result",
				Query:           "answer",
				ResultID:        "generate",
				From:            telego.User{ID: 42},
			},
		),
	)
	var source strings.Builder
	for n := 0; n < 300; n++ {
		fmt.Fprintf(
			&source,
			"BLOCK%04d\nRepeated paragraph %s\n\n```go\n%s\n```\n\n",
			n,
			strings.Repeat("shared ", 30),
			strings.Repeat("repeat(); ", 20),
		)
	}
	source.WriteString("LAST_POSITION")
	require.NoError(
		t,
		manager.SendMessage(
			t.Context(),
			bus.OutboundMessage{Channel: "telegram", ChatID: "inline:result", Content: source.String()},
		),
	)
	rejectLater = true
	require.NoError(
		t,
		channel.handleInlineCallbackQuery(
			(&th.Context{}).WithContext(t.Context()),
			telego.CallbackQuery{
				ID:              "later",
				InlineMessageID: "result",
				From:            telego.User{ID: 42},
				Data:            telegramInlinePagePrefix + "2",
			},
		),
	)
	marker := regexp.MustCompile(`BLOCK(\d{4})`)
	oldID := marker.FindStringSubmatch(rejected)
	newID := marker.FindStringSubmatch(plain)
	require.Len(t, oldID, 2)
	require.Len(t, newID, 2)
	oldPosition, err := strconv.Atoi(oldID[1])
	require.NoError(t, err)
	newPosition, err := strconv.Atoi(newID[1])
	require.NoError(t, err)
	require.Greater(t, oldPosition, 100)
	require.LessOrEqual(t, newPosition, oldPosition)
	require.LessOrEqual(
		t,
		oldPosition-newPosition,
		9,
		"plain pagination must retain the selected source position, not jump to page zero",
	)
	require.NotContains(t, plain, "BLOCK0000")
	pageCount := regexp.MustCompile(`Page \d+/(\d+)`).FindStringSubmatch(plain)
	require.Len(t, pageCount, 2)
	count, err := strconv.Atoi(pageCount[1])
	require.NoError(t, err)
	lastPage := strconv.Itoa(count - 1)
	require.NoError(
		t,
		channel.handleInlineCallbackQuery(
			(&th.Context{}).WithContext(t.Context()),
			telego.CallbackQuery{
				ID:              "last",
				InlineMessageID: "result",
				From:            telego.User{ID: 42},
				Data:            telegramInlinePagePrefix + lastPage,
			},
		),
	)
	require.Contains(t, plain, "LAST_POSITION")
}

func TestManagerMultipartFinalizeResumesWithoutRepeatingAcknowledgedChunks(t *testing.T) {
	for _, mode := range []string{"rejected_send", "ambiguous_send", "deleted_preview"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			sends := 0
			edits := 0
			acknowledged := map[string]int{}
			tailDelivered := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Thread int `json:"message_thread_id"`
					Rich   struct {
						Markdown string `json:"markdown"`
					} `json:"rich_message"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				mu.Lock()
				defer mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/editMessageText") {
					edits++
					if mode == "deleted_preview" && edits == 1 {
						_, _ = w.Write(
							[]byte(`{"ok":false,"error_code":400,"description":"message to edit not found"}`),
						)
						return
					}
					tailDelivered = tailDelivered || strings.Contains(body.Rich.Markdown, "TAIL_MARKER")
				}
				if strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
					sends++
					require.Equal(t, 7, body.Thread)
					if sends == 3 && mode == "rejected_send" {
						_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"temporary rejected send"}`))
						return
					}
					acknowledged[body.Rich.Markdown]++
					tailDelivered = tailDelivered || strings.Contains(body.Rich.Markdown, "TAIL_MARKER")
					if sends == 3 && mode == "ambiguous_send" {
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						_ = conn.Close()
						return
					}
				}
				_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, sends)
			}))
			defer server.Close()
			bot, err := telego.NewBot(
				testToken,
				telego.WithAPIServer(server.URL),
				telego.WithHTTPClient(server.Client()),
				telego.WithDiscardLogger(),
			)
			require.NoError(t, err)
			cfg := config.DefaultConfig()
			cfg.Channels.Telegram.Streaming.Enabled = true
			mb := bus.NewMessageBus()
			defer mb.Close()
			manager, err := channels.NewManager(cfg, mb, nil)
			require.NoError(t, err)
			channel := &TelegramChannel{
				bot:         bot,
				config:      cfg,
				BaseChannel: channels.NewBaseChannel("telegram", nil, mb, nil),
			}
			manager.RegisterChannel("telegram", outboundTelegram{channel})
			require.NoError(t, manager.StartAll(t.Context()))
			defer func() { require.NoError(t, manager.StopAll(t.Context())) }()
			ctx := bus.EnsureDeliveryID(bus.WithStreamReplyTo(t.Context(), "42"))
			stream, ok := manager.GetStreamer(ctx, "telegram", "-123/7")
			require.True(t, ok)
			require.NoError(t, stream.Update(ctx, "PREVIEW_MARKER"))
			var source strings.Builder
			for n := 0; n < 1800; n++ {
				fmt.Fprintf(&source, "ROW%04d %s\n", n, strings.Repeat("value ", 10))
			}
			source.WriteString("TAIL_MARKER")
			content := source.String()
			finalizeErr := stream.Finalize(ctx, content)
			msg := bus.OutboundMessage{
				DeliveryID:       bus.DeliveryID(ctx),
				Channel:          "telegram",
				ChatID:           "-123/7",
				ReplyToMessageID: "42",
				Content:          content,
			}
			deliveryErr := manager.SendMessage(ctx, msg)
			if mode == "ambiguous_send" {
				require.ErrorIs(t, deliveryErr, channels.ErrSendFailed)
			} else {
				require.NoError(t, deliveryErr)
			}
			if mode != "ambiguous_send" {
				require.NoError(t, stream.Finalize(ctx, content))
				require.NoError(t, manager.SendMessage(ctx, msg))
			}
			mu.Lock()
			for body, count := range acknowledged {
				require.Equal(t, 1, count, "acknowledged overflow was repeated: %q", body[:min(len(body), 40)])
			}
			count := sends
			tail := tailDelivered
			mu.Unlock()
			if mode == "ambiguous_send" {
				require.Error(t, finalizeErr)
				require.Equal(t, 3, count, "an ambiguous new send must not be retried or followed by a complete resend")
				require.False(t, tail)
				require.ErrorIs(t, channel.Send(ctx, msg), channels.ErrSendFailed)
			} else {
				require.True(
					t,
					tail,
					"remaining source must remain deliverable after a rejected send or deleted preview",
				)
				require.GreaterOrEqual(t, count, 4, "fixture must exercise at least three complete answer chunks")
			}
		})
	}
}

func TestManagerFailedPlaceholderDoesNotCaptureLaterIdenticalAnswer(t *testing.T) {
	var mu sync.Mutex
	rejected := false
	newRequestReplies := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reply struct {
				ID int `json:"message_id"`
			} `json:"reply_parameters"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			if !rejected {
				rejected = true
				_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"send forbidden"}`))
				return
			}
			if body.Reply.ID == 77 {
				newRequestReplies++
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":100}}`))
	}))
	defer server.Close()
	bot, err := telego.NewBot(
		testToken,
		telego.WithAPIServer(server.URL),
		telego.WithHTTPClient(server.Client()),
		telego.WithDiscardLogger(),
	)
	require.NoError(t, err)
	cfg := config.DefaultConfig()
	mb := bus.NewMessageBus()
	defer mb.Close()
	manager, err := channels.NewManager(cfg, mb, nil)
	require.NoError(t, err)
	channel := &TelegramChannel{bot: bot, config: cfg, BaseChannel: channels.NewBaseChannel("telegram", nil, mb, nil)}
	manager.RegisterChannel("telegram", outboundTelegram{channel})
	require.NoError(t, manager.StartAll(t.Context()))
	defer func() { require.NoError(t, manager.StopAll(t.Context())) }()
	manager.RecordPlaceholder("telegram", "-123/7", "99")
	msg := bus.OutboundMessage{
		Channel:          "telegram",
		ChatID:           "-123/7",
		ReplyToMessageID: "42",
		Content:          strings.Repeat("answer paragraph\n", 4500),
	}
	require.ErrorIs(t, manager.SendMessage(t.Context(), msg), channels.ErrSendFailed)
	msg.ReplyToMessageID = "77"
	require.NoError(t, manager.SendMessage(t.Context(), msg))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(
		t,
		1,
		newRequestReplies,
		"recovery for an earlier failed placeholder must not capture an identical answer for a new request",
	)
}
