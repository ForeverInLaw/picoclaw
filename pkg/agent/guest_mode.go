package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/utils"
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

func guestResponseQuote(msg bus.InboundMessage) string {
	if !isGuestMessage(msg) {
		return ""
	}

	query := strings.TrimSpace(msg.Metadata[telegramGuestQueryKey])
	if query == "" {
		query = strings.TrimSpace(msg.Content)
	}
	return query
}

func formatGuestResponse(quote, body string) string {
	return utils.FormatQuotedMessage(quote, body)
}
