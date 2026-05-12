package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

type getMeCaller struct {
	username string
}

func (c getMeCaller) Call(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
	if strings.HasSuffix(url, "/getMe") {
		result := fmt.Sprintf(`{"id":1,"is_bot":true,"first_name":"bot","username":%q}`, c.username)
		return &ta.Response{Ok: true, Result: []byte(result)}, nil
	}
	return &ta.Response{Ok: true, Result: []byte("true")}, nil
}

func newTestTelegramBot(t *testing.T, username string) *telego.Bot {
	t.Helper()

	token := "123456:" + strings.Repeat("a", 35)
	bot, err := telego.NewBot(token,
		telego.WithAPICaller(getMeCaller{username: username}),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("NewBot error: %v", err)
	}
	return bot
}

func newGroupMentionOnlyChannel(t *testing.T, botUsername string) (*TelegramChannel, *bus.MessageBus) {
	t.Helper()

	messageBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Channels.Telegram.ParticipantAliases = map[string]string{
		"10": "Сер",
		"77": "Визард",
	}
	cfg.Channels.Telegram.Batching.Enabled = false
	ch := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, messageBus, nil,
			channels.WithGroupTrigger(config.GroupTriggerConfig{MentionOnly: true}),
		),
		bot:     newTestTelegramBot(t, botUsername),
		config:  cfg,
		chatIDs: make(map[string]int64),
		ctx:     context.Background(),
	}
	return ch, messageBus
}

func TestHandleMessage_GroupMentionOnly_BotCommandEntity(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantForwarded bool
		wantContent   string
	}{
		{
			name:          "command with bot username",
			text:          "/new@testbot",
			wantForwarded: true,
			wantContent:   "/new",
		},
		{
			name:          "bare command",
			text:          "/new",
			wantForwarded: true,
			wantContent:   "/new",
		},
		{
			name:          "command for another bot",
			text:          "/new@otherbot",
			wantForwarded: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

			msg := &telego.Message{
				Text: tc.text,
				Entities: []telego.MessageEntity{{
					Type:   telego.EntityTypeBotCommand,
					Offset: 0,
					Length: len([]rune(tc.text)),
				}},
				MessageID: 42,
				Chat: telego.Chat{
					ID:   123,
					Type: "group",
				},
				From: &telego.User{
					ID:        7,
					FirstName: "Alice",
				},
			}

			if err := ch.handleMessage(context.Background(), msg); err != nil {
				t.Fatalf("handleMessage error: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
			defer cancel()
			select {
			case <-ctx.Done():
				if tc.wantForwarded {
					t.Fatal("timeout waiting for message to be forwarded")
					return
				}
			case inbound, ok := <-messageBus.InboundChan():
				if tc.wantForwarded {
					if !ok {
						t.Fatal("expected inbound message to be forwarded")
					}
					if inbound.Content != tc.wantContent {
						t.Fatalf("content=%q want=%q", inbound.Content, tc.wantContent)
					}
					return
				}
			}
		})
	}
}

func TestIsBotMentioned_MentionEntityUnaffected(t *testing.T) {
	ch, _ := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text: "@testbot hello",
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeMention,
			Offset: 0,
			Length: len("@testbot"),
		}},
	}

	if !ch.isBotMentioned(msg) {
		t.Fatal("expected mention entity to be treated as bot mention")
	}
}

func TestHandleMessage_GroupMentionOnly_ReplyToBot(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "без упоминания, но ответ боту",
		MessageID: 43,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        8,
			FirstName: "Bob",
		},
		ReplyToMessage: &telego.Message{
			MessageID: 42,
			From: &telego.User{
				ID:       1,
				Username: "testbot",
				IsBot:    true,
			},
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for reply-to-bot message to be forwarded")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if inbound.Content != msg.Text {
			t.Fatalf("content=%q want=%q", inbound.Content, msg.Text)
		}
	}
}

func TestHandleMessage_GroupMentionOnly_NameTrigger(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")
	ch.config.Channels.Telegram.NameTriggers = []string{"короб"}

	msg := &telego.Message{
		Text:      "что думаешь, коробки?",
		MessageID: 46,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        8,
			FirstName: "Bob",
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for name-triggered message to be forwarded")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if inbound.Content != msg.Text {
			t.Fatalf("content=%q want=%q", inbound.Content, msg.Text)
		}
		if got := inbound.Metadata["observe_only"]; got != "" {
			t.Fatalf("observe_only=%q want empty", got)
		}
	}
}

func TestHandleMessage_GroupMentionOnly_NameTriggerFromCaption(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")
	ch.config.Channels.Telegram.NameTriggers = []string{"короб"}

	msg := &telego.Message{
		Caption:   "Коробка, посмотри на это",
		MessageID: 47,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        9,
			FirstName: "Carol",
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for caption name-triggered message to be forwarded")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if !strings.Contains(inbound.Content, "Коробка, посмотри на это") {
			t.Fatalf("content=%q missing caption", inbound.Content)
		}
		if got := inbound.Metadata["observe_only"]; got != "" {
			t.Fatalf("observe_only=%q want empty", got)
		}
	}
}

func TestHandleMessage_GroupMentionOnly_ReplyToHuman_ObservedOnly(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "ответ не боту",
		MessageID: 44,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        9,
			FirstName: "Carol",
		},
		ReplyToMessage: &telego.Message{
			MessageID: 41,
			From: &telego.User{
				ID:        77,
				Username:  "alice",
				FirstName: "Alice",
				IsBot:     false,
			},
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for passive group message to be forwarded")
	case inbound := <-messageBus.InboundChan():
		if got := inbound.Metadata["observe_only"]; got != "true" {
			t.Fatalf("observe_only=%q want=true", got)
		}
		if inbound.Content != "ответ не боту" {
			t.Fatalf("content=%q want=%q", inbound.Content, "ответ не боту")
		}
	}
}

func TestHandleMessage_GroupMentionOnly_ReplyToOtherBotWithoutUsername_ObservedOnly(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "ответ не нашему боту",
		MessageID: 48,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        9,
			FirstName: "Carol",
		},
		ReplyToMessage: &telego.Message{
			MessageID: 41,
			From: &telego.User{
				ID:    999,
				IsBot: true,
			},
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for passive group message to be forwarded")
	case inbound := <-messageBus.InboundChan():
		if got := inbound.Metadata["observe_only"]; got != "true" {
			t.Fatalf("observe_only=%q want=true", got)
		}
		if got := inbound.Metadata["trigger_reason"]; got != "observe_only" {
			t.Fatalf("trigger_reason=%q want=%q", got, "observe_only")
		}
	}
}

func TestHandleMessage_GroupMentionOnly_ReplyToBot_SetsTriggerReason(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "без упоминания, но ответ боту",
		MessageID: 49,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		From: &telego.User{
			ID:        8,
			FirstName: "Bob",
		},
		ReplyToMessage: &telego.Message{
			MessageID: 42,
			From: &telego.User{
				ID:       1,
				Username: "testbot",
				IsBot:    true,
			},
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for reply-to-bot message to be forwarded")
	case inbound := <-messageBus.InboundChan():
		if got := inbound.Metadata["trigger_reason"]; got != "reply_to_bot" {
			t.Fatalf("trigger_reason=%q want=%q", got, "reply_to_bot")
		}
	}
}

func TestHandleMessage_GroupMentionReply_IncludesQuotedContext(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "@testbot согласен",
		MessageID: 45,
		Chat: telego.Chat{
			ID:   123,
			Type: "group",
		},
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeMention,
			Offset: 0,
			Length: len("@testbot"),
		}},
		From: &telego.User{
			ID:        10,
			FirstName: "Dave",
		},
		ReplyToMessage: &telego.Message{
			MessageID: 41,
			Text:      "исходное сообщение",
			From: &telego.User{
				ID:        77,
				Username:  "alice",
				FirstName: "Alice",
			},
		},
	}

	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for mention reply to be forwarded")
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		wantContent := "[quoted user message from alice]: исходное сообщение\n\nсогласен"
		if inbound.Content != wantContent {
			t.Fatalf("content=%q want=%q", inbound.Content, wantContent)
		}
		if got := inbound.Sender.DisplayName; got != "Сер" {
			t.Fatalf("sender display name=%q want=%q", got, "Сер")
		}
		if got := inbound.Metadata["sender_label"]; got != "Сер" {
			t.Fatalf("sender_label=%q want=%q", got, "Сер")
		}
		if got := inbound.Metadata["sender_alias"]; got != "Сер" {
			t.Fatalf("sender_alias=%q want=%q", got, "Сер")
		}
		if got := inbound.Metadata["reply_to_message_id"]; got != "41" {
			t.Fatalf("reply_to_message_id=%q want=%q", got, "41")
		}
		if got := inbound.Metadata["reply_to_user_id"]; got != "77" {
			t.Fatalf("reply_to_user_id=%q want=%q", got, "77")
		}
		if got := inbound.Metadata["reply_to_sender_id"]; got != "telegram:77" {
			t.Fatalf("reply_to_sender_id=%q want=%q", got, "telegram:77")
		}
		if got := inbound.Metadata["reply_to_username"]; got != "alice" {
			t.Fatalf("reply_to_username=%q want=%q", got, "alice")
		}
		if got := inbound.Metadata["reply_to_first_name"]; got != "Alice" {
			t.Fatalf("reply_to_first_name=%q want=%q", got, "Alice")
		}
		if got := inbound.Metadata["reply_to_label"]; got != "Визард" {
			t.Fatalf("reply_to_label=%q want=%q", got, "Визард")
		}
		if got := inbound.Metadata["reply_to_alias"]; got != "Визард" {
			t.Fatalf("reply_to_alias=%q want=%q", got, "Визард")
		}
	}
}

func TestMergeQuotedTelegramReplyMedia_PrependsQuotedAttachments(t *testing.T) {
	quoted := []string{"media://quoted-image", "media://quoted-file"}
	current := []string{"media://current-image"}

	got := mergeQuotedTelegramReplyMedia(quoted, current)
	want := []string{"media://quoted-image", "media://quoted-file", "media://current-image"}
	if len(got) != len(want) {
		t.Fatalf("len(media)=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("media[%d]=%q want=%q (all=%v)", i, got[i], want[i], got)
		}
	}
}
