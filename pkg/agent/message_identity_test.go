package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestBuildConversationUserMessage_TelegramGroupIncludesIdentityEnvelope(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "telegram:6669548787",
		Sender: bus.SenderInfo{
			CanonicalID: "telegram:6669548787",
			DisplayName: "Сер",
			Username:    "nevermore",
		},
		Content: "сработало?",
		Metadata: map[string]string{
			"is_group":           "true",
			"sender_label":       "Сер",
			"username":           "nevermore",
			"reply_to_label":     "Визард",
			"reply_to_sender_id": "telegram:480546776",
		},
	}

	got := buildConversationUserMessage(msg)
	want := "[telegram_group_message]\n" +
		"sender_label: Сер\n" +
		"sender_id: telegram:6669548787\n" +
		"sender_username: nevermore\n" +
		"reply_to_label: Визард\n" +
		"reply_to_id: telegram:480546776\n\n" +
		"сработало?"

	if got != want {
		t.Fatalf("buildConversationUserMessage() = %q, want %q", got, want)
	}
}

func TestBuildConversationUserMessage_NonTelegramReturnsRawContent(t *testing.T) {
	msg := bus.InboundMessage{
		Channel: "email",
		Content: "hello",
	}

	if got := buildConversationUserMessage(msg); got != "hello" {
		t.Fatalf("buildConversationUserMessage() = %q, want %q", got, "hello")
	}
}
