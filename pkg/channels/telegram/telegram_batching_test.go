package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newBatchingTestChannel(t *testing.T, windowMS int) (*TelegramChannel, *bus.MessageBus) {
	t.Helper()

	messageBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Channels.Telegram.Batching.Enabled = true
	cfg.Channels.Telegram.Batching.WindowMS = windowMS
	ch := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, messageBus, nil),
		bot:         newTestTelegramBot(t, "testbot"),
		config:      cfg,
		chatIDs:     make(map[string]int64),
		ctx:         context.Background(),
		batches:     make(map[string]*telegramInboundBatch),
	}
	return ch, messageBus
}

func recvInbound(t *testing.T, ch <-chan bus.InboundMessage, timeout time.Duration) bus.InboundMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for inbound message")
		return bus.InboundMessage{}
	}
}

func TestHandleMessage_BatchesPlainTextWithinWindow(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	msg1 := &telego.Message{
		Text:      "первая часть",
		MessageID: 101,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}
	msg2 := &telego.Message{
		Text:      "вторая часть",
		MessageID: 102,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("handleMessage(msg2) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if inbound.Content != "первая часть\n\nвторая часть" {
		t.Fatalf("content=%q", inbound.Content)
	}
	if inbound.MessageID != "101" {
		t.Fatalf("messageID=%q want=101", inbound.MessageID)
	}
	if got := inbound.Metadata["batch_count"]; got != "2" {
		t.Fatalf("batch_count=%q want=2", got)
	}
	if got := inbound.Metadata["batch_message_ids"]; got != "101,102" {
		t.Fatalf("batch_message_ids=%q want=101,102", got)
	}
}

func TestHandleMessage_CommandBypassesBatching(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 50)

	msg1 := &telego.Message{
		Text:      "обычный текст",
		MessageID: 201,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}
	cmd := &telego.Message{
		Text:      "/new",
		MessageID: 202,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeBotCommand,
			Offset: 0,
			Length: len("/new"),
		}},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), cmd); err != nil {
		t.Fatalf("handleMessage(cmd) error: %v", err)
	}

	first := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	second := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)

	if first.Content != "обычный текст" {
		t.Fatalf("first content=%q want ordinary text", first.Content)
	}
	if second.Content != "/new" {
		t.Fatalf("second content=%q want /new", second.Content)
	}
}

func TestHandleMessage_ForwardedMessagesAndOwnCommentStayLabeled(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	forward1 := &telego.Message{
		Text:      "первый пересланный",
		MessageID: 301,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ForwardOrigin: &telego.MessageOriginUser{
			SenderUser: telego.User{ID: 7, Username: "bob", FirstName: "Bob"},
		},
	}
	forward2 := &telego.Message{
		Text:      "второй пересланный",
		MessageID: 302,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ForwardOrigin: &telego.MessageOriginHiddenUser{
			SenderUserName: "Charlie",
		},
	}
	own := &telego.Message{
		Text:      "мой комментарий",
		MessageID: 303,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}

	if err := ch.handleMessage(context.Background(), forward1); err != nil {
		t.Fatalf("handleMessage(forward1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), forward2); err != nil {
		t.Fatalf("handleMessage(forward2) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), own); err != nil {
		t.Fatalf("handleMessage(own) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	wantParts := []string{
		"[forwarded from bob]: первый пересланный",
		"[forwarded from Charlie]: второй пересланный",
		"мой комментарий",
	}
	for _, want := range wantParts {
		if !strings.Contains(inbound.Content, want) {
			t.Fatalf("content=%q missing %q", inbound.Content, want)
		}
	}
	if got := inbound.Metadata["batch_count"]; got != "3" {
		t.Fatalf("batch_count=%q want=3", got)
	}
}

func TestHandleMessage_ReplyBoundaryFlushesBatch(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	msg1 := &telego.Message{
		Text:      "ответ на первое",
		MessageID: 401,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ReplyToMessage: &telego.Message{
			MessageID: 1,
			Text:      "msg1",
			From:      &telego.User{ID: 77, FirstName: "Bob"},
		},
	}
	msg2 := &telego.Message{
		Text:      "ответ на второе",
		MessageID: 402,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ReplyToMessage: &telego.Message{
			MessageID: 2,
			Text:      "msg2",
			From:      &telego.User{ID: 88, FirstName: "Carol"},
		},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("handleMessage(msg2) error: %v", err)
	}

	first := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	second := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if first.Metadata["reply_to_message_id"] != "1" {
		t.Fatalf("first reply_to_message_id=%q want=1", first.Metadata["reply_to_message_id"])
	}
	if second.Metadata["reply_to_message_id"] != "2" {
		t.Fatalf("second reply_to_message_id=%q want=2", second.Metadata["reply_to_message_id"])
	}
}
