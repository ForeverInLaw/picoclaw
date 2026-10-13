package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const telegramInlineMetadataKey = "telegram_inline"
const telegramInlineQueryKey = "inline_query"

func telegramUserID(msg bus.InboundMessage) string {
	raw := firstNonEmpty(msg.Metadata["telegram_user_id"], msg.Sender.PlatformID, strings.TrimPrefix(msg.SenderID, "telegram:"))
	uid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || uid <= 0 {
		return ""
	}
	return strconv.FormatInt(uid, 10)
}

func inlineConversationKey(agentID, userID string) string {
	return fmt.Sprintf("agent:%s:telegram:inline:user:%s", agentID, userID)
}

var inlineSafeTools = []string{"web_search", "web_fetch", "fact_check"}

func isInlineMessage(msg bus.InboundMessage) bool {
	if strings.EqualFold(msg.Metadata[telegramInlineMetadataKey], "true") {
		return true
	}
	return msg.Channel == "telegram" && strings.HasPrefix(strings.TrimSpace(msg.ChatID), "inline:")
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
