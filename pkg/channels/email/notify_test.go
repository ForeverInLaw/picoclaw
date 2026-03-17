package email

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNotifyInboundMessage_PublishesToConfiguredTelegramChats(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := &EmailChannel{
		BaseChannel: channels.NewBaseChannel("email", nil, msgBus, nil),
		config: config.EmailConfig{
			NotifyTelegramIDs: config.FlexibleStringSlice{"480546776", "6669548787"},
		},
	}

	msg := incomingMessage{
		ChatID:     "sender@example.com",
		SenderName: "Sender",
		Subject:    "Status update",
		Content:    "Email received.\nFrom: Sender <sender@example.com>\nSubject: Status update\n\nBody:\nPlease review the latest update.",
	}

	if err := ch.notifyInboundMessage(msg); err != nil {
		t.Fatalf("notifyInboundMessage returned error: %v", err)
	}

	got1 := readOutbound(t, msgBus)
	got2 := readOutbound(t, msgBus)
	if got1.Channel != "telegram" || got2.Channel != "telegram" {
		t.Fatal("expected telegram notifications")
	}
	if got1.ChatID != "480546776" {
		t.Fatalf("unexpected first chat id: %q", got1.ChatID)
	}
	if got2.ChatID != "6669548787" {
		t.Fatalf("unexpected second chat id: %q", got2.ChatID)
	}
	if !strings.Contains(got1.Content, "Please review the latest update.") {
		t.Fatalf("notification summary missing body text: %q", got1.Content)
	}
	if !strings.Contains(got1.Content, "Статус: рутинное") {
		t.Fatalf("notification missing status: %q", got1.Content)
	}
}

func TestExtractNotificationSummary_TruncatesBody(t *testing.T) {
	longBody := "Email received.\n\nBody:\n" + strings.Repeat("a", maxNotificationSummaryLength+25)

	got := extractNotificationSummary(longBody)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated summary, got %q", got)
	}
	if len([]rune(got)) != maxNotificationSummaryLength+3 {
		t.Fatalf("unexpected summary length: %d", len([]rune(got)))
	}
}

func readOutbound(t *testing.T, msgBus *bus.MessageBus) bus.OutboundMessage {
	t.Helper()

	select {
	case msg := <-msgBus.OutboundChan():
		return msg
	case <-context.Background().Done():
		t.Fatal("context should not be done")
	}

	return bus.OutboundMessage{}
}
