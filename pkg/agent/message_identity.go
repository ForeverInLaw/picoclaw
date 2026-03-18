package agent

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func buildConversationUserMessage(msg bus.InboundMessage) string {
	content := strings.TrimSpace(msg.Content)
	if msg.Channel != "telegram" || msg.Metadata == nil || msg.Metadata["is_group"] != "true" {
		return content
	}

	senderLabel := firstNonEmpty(
		msg.Metadata["sender_label"],
		msg.Sender.DisplayName,
		msg.Sender.Username,
		msg.SenderID,
	)

	var sb strings.Builder
	sb.WriteString("[telegram_group_message]\n")
	fmt.Fprintf(&sb, "sender_label: %s\n", senderLabel)
	if senderID := strings.TrimSpace(msg.SenderID); senderID != "" {
		fmt.Fprintf(&sb, "sender_id: %s\n", senderID)
	}
	if username := strings.TrimSpace(msg.Metadata["username"]); username != "" {
		fmt.Fprintf(&sb, "sender_username: %s\n", username)
	}

	replyLabel := strings.TrimSpace(msg.Metadata["reply_to_label"])
	replyID := strings.TrimSpace(msg.Metadata["reply_to_sender_id"])
	if replyLabel != "" {
		fmt.Fprintf(&sb, "reply_to_label: %s\n", replyLabel)
	}
	if replyID != "" {
		fmt.Fprintf(&sb, "reply_to_id: %s\n", replyID)
	}

	sb.WriteString("\n")
	sb.WriteString(content)
	return sb.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
