package email

import (
	"net/mail"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestShouldIgnoreMessage_AllowsNormalEmailWithoutListHeaders(t *testing.T) {
	msg := incomingMessage{
		ChatID: "sender@example.com",
		RawHeader: formatFilterHeader(
			&mail.Address{Address: "sender@example.com"},
			"Hello",
			time.Date(2026, time.March, 17, 18, 0, 0, 0, time.UTC),
			"<msg-1>",
			"",
			"",
			"",
			"",
		),
	}

	ch := &EmailChannel{
		config: config.EmailConfig{
			Address:  "contact@redstone.md",
			Username: "contact@redstone.md",
		},
	}

	if ch.shouldIgnoreMessage(msg) {
		t.Fatal("normal email should not be ignored")
	}
}

func TestShouldIgnoreMessage_ListIDStillIgnored(t *testing.T) {
	msg := incomingMessage{
		ChatID: "sender@example.com",
		RawHeader: formatFilterHeader(
			&mail.Address{Address: "sender@example.com"},
			"Hello",
			time.Date(2026, time.March, 17, 18, 0, 0, 0, time.UTC),
			"<msg-1>",
			"",
			"",
			"list.example.com",
			"",
		),
	}

	ch := &EmailChannel{
		config: config.EmailConfig{
			Address:  "contact@redstone.md",
			Username: "contact@redstone.md",
		},
	}

	if !ch.shouldIgnoreMessage(msg) {
		t.Fatal("list email should be ignored")
	}
}
