package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

const telegramInlineMetadataKey = "telegram_inline"

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
