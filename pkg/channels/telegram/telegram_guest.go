package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	telegramGuestChatIDPrefix = "guest:"
	telegramGuestMetadataKey  = "telegram_guest"
	telegramGuestQueryKey     = "guest_query"
)

func isTelegramGuestChatID(chatID string) bool {
	return strings.HasPrefix(strings.TrimSpace(chatID), telegramGuestChatIDPrefix)
}

func telegramGuestChatID(inlineMessageID string) string {
	return telegramGuestChatIDPrefix + strings.TrimSpace(inlineMessageID)
}

func telegramGuestInlineMessageID(chatID string) (string, bool) {
	if !isTelegramGuestChatID(chatID) {
		return "", false
	}
	inlineMessageID := strings.TrimSpace(strings.TrimPrefix(chatID, telegramGuestChatIDPrefix))
	return inlineMessageID, inlineMessageID != ""
}

func (c *TelegramChannel) telegramSenderInfo(user *telego.User) bus.SenderInfo {
	if user == nil {
		return bus.SenderInfo{}
	}

	platformID := fmt.Sprintf("%d", user.ID)
	return bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    user.Username,
		DisplayName: c.resolveParticipantLabel(user),
	}
}

// buildTelegramGuestPlaceholderResult constructs the InlineQueryResultArticle that
// answerGuestQuery posts immediately, before the agent has any output. The article's
// text is what the user sees while the bot streams its real response via
// EditMessageText against the returned inline_message_id.
func buildTelegramGuestPlaceholderResult(cfg config.TelegramGuestConfig, query string, useMarkdownV2 bool) telego.InlineQueryResult {
	title := strings.TrimSpace(cfg.ResultTitle)
	if title == "" {
		title = "Сгенерировать ответ"
	}

	placeholder := normalizeTelegramGuestPlaceholder(cfg.PlaceholderText)

	parseMode := telego.ModeMarkdownV2
	content := renderTelegramGuestInitialContent(query, placeholder)
	if !useMarkdownV2 {
		parseMode = telego.ModeHTML
		content = parseContent(renderTelegramGuestInitialContentPlain(query, placeholder), false)
	}

	return &telego.InlineQueryResultArticle{
		Type:  telego.ResultTypeArticle,
		ID:    "generate",
		Title: title,
		InputMessageContent: &telego.InputTextMessageContent{
			MessageText: content,
			ParseMode:   parseMode,
		},
	}
}

func normalizeTelegramGuestPlaceholder(placeholder string) string {
	placeholder = strings.TrimSpace(placeholder)
	if placeholder == "" {
		return "Думаю... 💭"
	}
	if placeholder == "Думаю..." || placeholder == "Думаю…" {
		return placeholder + " 💭"
	}
	return placeholder
}

func renderTelegramGuestInitialContent(query, placeholder string) string {
	query = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(query, "\r\n", "\n"), "\r", "\n"))
	placeholder = strings.TrimSpace(placeholder)

	quotedLines := make([]string, 0)
	if query != "" {
		for _, line := range strings.Split(query, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				quotedLines = append(quotedLines, ">")
				continue
			}
			quotedLines = append(quotedLines, "> "+escapeMarkdownV2(line))
		}
	}

	quoted := strings.Join(quotedLines, "\n")
	body := escapeMarkdownV2(placeholder)
	switch {
	case quoted == "":
		return body
	case body == "":
		return quoted
	default:
		return quoted + "\n\n" + body
	}
}

func renderTelegramGuestInitialContentPlain(query, placeholder string) string {
	query = strings.TrimSpace(query)
	placeholder = strings.TrimSpace(placeholder)
	switch {
	case query == "":
		return placeholder
	case placeholder == "":
		return query
	default:
		return query + "\n\n" + placeholder
	}
}

func buildTelegramGuestInboundMessage(msg *telego.Message, inlineMessageID string, sender bus.SenderInfo) bus.InboundMessage {
	query := strings.TrimSpace(telegramMessageText(msg))

	return bus.InboundMessage{
		Channel:  "telegram",
		SenderID: sender.CanonicalID,
		Sender:   sender,
		ChatID:   telegramGuestChatID(inlineMessageID),
		Content:  query,
		Peer: bus.Peer{
			Kind: "direct",
			ID:   sender.CanonicalID,
		},
		MessageID: inlineMessageID,
		Metadata: map[string]string{
			telegramGuestMetadataKey: "true",
			telegramGuestQueryKey:    query,
		},
	}
}

// handleGuestMessage is called for every Update.GuestMessage. It answers the guest
// query immediately with a placeholder article, captures the returned
// inline_message_id, then publishes an inbound message keyed by that ID so the
// agent loop streams its real response via EditMessageText.
func (c *TelegramChannel) handleGuestMessage(ctx *th.Context, msg telego.Message) error {
	guestCfg := c.config.Channels.Telegram.Guest
	if !guestCfg.Enabled {
		return nil
	}
	if strings.TrimSpace(msg.GuestQueryID) == "" {
		return nil
	}

	sender := c.telegramSenderInfo(msg.From)
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Guest message rejected by allowlist", map[string]any{
			"sender_id": sender.CanonicalID,
		})
		return nil
	}

	query := strings.TrimSpace(telegramMessageText(&msg))
	result := buildTelegramGuestPlaceholderResult(guestCfg, query, c.config.Channels.Telegram.UseMarkdownV2)

	answered, err := ctx.Bot().AnswerGuestQuery(ctx, &telego.AnswerGuestQueryParams{
		GuestQueryID: msg.GuestQueryID,
		Result:       result,
	})
	if err != nil {
		logger.WarnCF("telegram", "answerGuestQuery failed", map[string]any{
			"guest_query_id": msg.GuestQueryID,
			"error":          err.Error(),
		})
		return nil
	}
	if answered == nil || strings.TrimSpace(answered.InlineMessageID) == "" {
		logger.WarnC("telegram", "answerGuestQuery returned empty inline_message_id")
		return nil
	}

	return c.PublishInbound(ctx, buildTelegramGuestInboundMessage(&msg, answered.InlineMessageID, sender))
}

