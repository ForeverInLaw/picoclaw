package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
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
