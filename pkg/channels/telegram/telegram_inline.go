package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	telegramInlineChatIDPrefix = "inline:"
	telegramInlineCallbackData = "__picoclaw_inline_ack__"
	telegramInlineMetadataKey  = "telegram_inline"
	telegramInlineQueryKey     = "inline_query"
)

func isTelegramInlineChatID(chatID string) bool {
	return strings.HasPrefix(strings.TrimSpace(chatID), telegramInlineChatIDPrefix)
}

func telegramInlineChatID(inlineMessageID string) string {
	return telegramInlineChatIDPrefix + strings.TrimSpace(inlineMessageID)
}

func telegramInlineMessageID(chatID string) (string, bool) {
	if !isTelegramInlineChatID(chatID) {
		return "", false
	}
	inlineMessageID := strings.TrimSpace(strings.TrimPrefix(chatID, telegramInlineChatIDPrefix))
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

func buildTelegramInlineQueryResult(cfg config.TelegramInlineConfig, query string, useMarkdownV2 bool) telego.InlineQueryResult {
	title := strings.TrimSpace(cfg.ResultTitle)
	if title == "" {
		title = "Сгенерировать ответ"
	}

	placeholder := strings.TrimSpace(cfg.PlaceholderText)
	if placeholder == "" {
		placeholder = "Думаю..."
	}

	buttonLabel := strings.TrimSpace(cfg.ButtonLabel)
	if buttonLabel == "" {
		buttonLabel = "📦"
	}

	description := strings.TrimSpace(query)
	if description != "" {
		description = utils.Truncate(description, 96)
	}

	return &telego.InlineQueryResultArticle{
		Type:  telego.ResultTypeArticle,
		ID:    "generate",
		Title: title,
		InputMessageContent: &telego.InputTextMessageContent{
			MessageText: renderTelegramInlineInitialContent(query, placeholder, useMarkdownV2),
			ParseMode:   telegramParseMode(useMarkdownV2),
		},
		ReplyMarkup: &telego.InlineKeyboardMarkup{
			InlineKeyboard: [][]telego.InlineKeyboardButton{{
				{
					Text:         buttonLabel,
					CallbackData: telegramInlineCallbackData,
				},
			}},
		},
		Description: description,
	}
}

func buildTelegramInlineInboundMessage(result telego.ChosenInlineResult, sender bus.SenderInfo) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:  "telegram",
		SenderID: sender.CanonicalID,
		Sender:   sender,
		ChatID:   telegramInlineChatID(result.InlineMessageID),
		Content:  strings.TrimSpace(result.Query),
		Peer: bus.Peer{
			Kind: "direct",
			ID:   sender.CanonicalID,
		},
		MessageID: result.ResultID,
		Metadata: map[string]string{
			telegramInlineMetadataKey: "true",
			telegramInlineQueryKey:    strings.TrimSpace(result.Query),
		},
	}
}

func (c *TelegramChannel) handleInlineQuery(ctx *th.Context, query telego.InlineQuery) error {
	inlineCfg := c.config.Channels.Telegram.Inline
	if !inlineCfg.Enabled {
		return ctx.Bot().AnswerInlineQuery(ctx, &telego.AnswerInlineQueryParams{
			InlineQueryID: query.ID,
			Results:       []telego.InlineQueryResult{},
		})
	}

	sender := c.telegramSenderInfo(&query.From)
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Inline query rejected by allowlist", map[string]any{
			"sender_id": sender.CanonicalID,
		})
		return ctx.Bot().AnswerInlineQuery(ctx, &telego.AnswerInlineQueryParams{
			InlineQueryID: query.ID,
			Results:       []telego.InlineQueryResult{},
		})
	}

	trimmedQuery := strings.TrimSpace(query.Query)
	results := []telego.InlineQueryResult{}
	if trimmedQuery != "" {
		results = append(results, buildTelegramInlineQueryResult(inlineCfg, trimmedQuery, c.config.Channels.Telegram.UseMarkdownV2))
	}

	params := &telego.AnswerInlineQueryParams{
		InlineQueryID: query.ID,
		Results:       results,
		CacheTime:     inlineCfg.CacheTimeSeconds,
		IsPersonal:    inlineCfg.IsPersonal,
	}
	return ctx.Bot().AnswerInlineQuery(ctx, params)
}

func (c *TelegramChannel) handleChosenInlineResult(ctx *th.Context, result telego.ChosenInlineResult) error {
	inlineCfg := c.config.Channels.Telegram.Inline
	if !inlineCfg.Enabled {
		return nil
	}
	if strings.TrimSpace(result.InlineMessageID) == "" || strings.TrimSpace(result.Query) == "" {
		return nil
	}

	sender := c.telegramSenderInfo(&result.From)
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Chosen inline result rejected by allowlist", map[string]any{
			"sender_id": sender.CanonicalID,
		})
		return nil
	}

	return c.PublishInbound(ctx, buildTelegramInlineInboundMessage(result, sender))
}

func renderTelegramInlineInitialContent(query, placeholder string, useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return utils.FormatQuotedMessage(query, placeholder)
	}

	escapedQuery := escapeHTML(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(query, "\r\n", "\n"), "\r", "\n")))
	escapedQuery = strings.ReplaceAll(escapedQuery, "\n", "<br>")
	escapedPlaceholder := escapeHTML(strings.TrimSpace(placeholder))
	if escapedQuery == "" {
		return escapedPlaceholder
	}
	if escapedPlaceholder == "" {
		return "<blockquote>" + escapedQuery + "</blockquote>"
	}
	return "<blockquote>" + escapedQuery + "</blockquote>\n\n" + escapedPlaceholder
}

func (c *TelegramChannel) handleInlineCallbackQuery(ctx *th.Context, query telego.CallbackQuery) error {
	if query.Data != telegramInlineCallbackData {
		return nil
	}
	return ctx.Bot().AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID))
}
