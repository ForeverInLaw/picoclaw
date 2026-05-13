package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

const telegramGuestMetadataKey = "telegram_guest"
const telegramGuestQueryKey = "guest_query"

var guestSafeTools = []string{
	"web_search",
	"web_fetch",
	"fact_check",
	"exec",
	"read_file",
	"write_file",
	"list_dir",
	"cron",
}

func isGuestMessage(msg bus.InboundMessage) bool {
	if strings.EqualFold(msg.Metadata[telegramGuestMetadataKey], "true") {
		return true
	}
	return isGuestChatTarget(msg.Channel, msg.ChatID)
}

func isGuestChatTarget(channel, chatID string) bool {
	return channel == "telegram" && strings.HasPrefix(strings.TrimSpace(chatID), "guest:")
}

func guestToolAllowlist() []string {
	return append([]string(nil), guestSafeTools...)
}

// guestResponseQuote previously returned the user query so the bot could echo it
// above its answer. In Guest Mode the user's original message is already visible
// in the chat above the bot's reply, so echoing is redundant and the helper now
// always returns the empty string. Kept as a stable seam in case future API
// changes (e.g. cross-chat answers) re-introduce the need.
func guestResponseQuote(_ bus.InboundMessage) string {
	return ""
}

// formatGuestResponse returns the agent's body unchanged. Quote echoing is no
// longer needed in Guest Mode (see guestResponseQuote).
func formatGuestResponse(_ string, body string) string {
	return body
}
