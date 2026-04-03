package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const telegramInlineMetadataKey = "telegram_inline"
const telegramInlineQueryKey = "inline_query"

var inlineSafeTools = []string{"web_search", "web_fetch", "fact_check"}

func isInlineMessage(msg bus.InboundMessage) bool {
	if strings.EqualFold(msg.Metadata[telegramInlineMetadataKey], "true") {
		return true
	}
	return isInlineChatTarget(msg.Channel, msg.ChatID)
}

func isInlineChatTarget(channel, chatID string) bool {
	return channel == "telegram" && strings.HasPrefix(strings.TrimSpace(chatID), "inline:")
}

func inlineToolAllowlist() []string {
	return append([]string(nil), inlineSafeTools...)
}

func inlineResponseQuote(msg bus.InboundMessage) string {
	if !isInlineMessage(msg) {
		return ""
	}

	query := strings.TrimSpace(msg.Metadata[telegramInlineQueryKey])
	if query == "" {
		query = strings.TrimSpace(msg.Content)
	}
	return query
}

func formatInlineResponse(quote, body string) string {
	return utils.FormatQuotedMessage(quote, body)
}
