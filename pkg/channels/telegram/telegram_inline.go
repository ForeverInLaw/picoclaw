package telegram

import (
	"fmt"
	"os"
	"strconv"
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
	telegramGuestMetadataKey   = "telegram_guest"
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

func buildTelegramInlineQueryResult(
	cfg config.TelegramInlineConfig,
	query string,
	useMarkdownV2 bool,
) telego.InlineQueryResult {
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
			"telegram_user_id":        fmt.Sprintf("%d", result.From.ID),
		},
	}
}

// Guest mentions publish directly into their destination and then use the same
// isolated conversation and retained-answer lifecycle as selected inline results.
func (c *TelegramChannel) handleGuestMessage(ctx *th.Context, msg telego.Message) error {
	if !c.config.Channels.Telegram.Inline.Enabled || strings.TrimSpace(msg.GuestQueryID) == "" || msg.From == nil {
		return nil
	}
	sender := c.telegramSenderInfo(msg.From)
	if !c.IsAllowedSender(sender) {
		return nil
	}
	query := strings.TrimSpace(msg.Text)
	if query == "" {
		query = strings.TrimSpace(msg.Caption)
	}
	if query == "" {
		return nil
	}
	result := buildTelegramInlineQueryResult(c.config.Channels.Telegram.Inline, "", c.config.Channels.Telegram.UseMarkdownV2).(*telego.InlineQueryResultArticle)
	result.ReplyMarkup = nil
	answered, err := c.bot.AnswerGuestQuery(ctx, &telego.AnswerGuestQueryParams{
		GuestQueryID: msg.GuestQueryID,
		Result:       result,
	})
	if err != nil {
		return fmt.Errorf("answer guest query: %w", err)
	}
	if answered == nil || strings.TrimSpace(answered.InlineMessageID) == "" {
		return fmt.Errorf("answer guest query returned no inline message ID")
	}
	return c.handleInlineResult(ctx, telego.ChosenInlineResult{
		ResultID: result.ID, InlineMessageID: answered.InlineMessageID,
		Query: query, From: *msg.From,
	}, true)
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
		results = append(
			results,
			buildTelegramInlineQueryResult(inlineCfg, trimmedQuery, c.config.Channels.Telegram.UseMarkdownV2),
		)
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
	return c.handleInlineResult(ctx, result, false)
}

func (c *TelegramChannel) handleInlineResult(ctx *th.Context, result telego.ChosenInlineResult, guest bool) error {
	inlineCfg := c.config.Channels.Telegram.Inline
	if !inlineCfg.Enabled {
		return nil
	}
	if strings.TrimSpace(result.InlineMessageID) == "" || strings.TrimSpace(result.Query) == "" || result.From.ID <= 0 {
		return nil
	}

	sender := c.telegramSenderInfo(&result.From)
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Chosen inline result rejected by allowlist", map[string]any{
			"sender_id": sender.CanonicalID,
		})
		return nil
	}

	c.inlineMu.Lock()
	previous, readErr := c.loadInlinePages(result.InlineMessageID)
	if readErr == nil {
		if previous.OwnerID != result.From.ID {
			c.inlineMu.Unlock()
			return fmt.Errorf("inline result belongs to another user")
		}
		if previous.Content != "" {
			if !previous.Published {
				err := c.publishInlinePages(ctx, result.InlineMessageID, previous, true)
				c.inlineMu.Unlock()
				return err
			}
			c.inlineMu.Unlock()
			return nil
		}
	} else if !os.IsNotExist(readErr) {
		c.inlineMu.Unlock()
		return fmt.Errorf("read selected inline result identity: %w", readErr)
	}
	state := c.inlineReplyLocked(result.InlineMessageID)
	if state.admitted {
		c.inlineMu.Unlock()
		return nil
	}
	if err := c.saveInlinePages(result.InlineMessageID, telegramInlinePages{OwnerID: result.From.ID}); err != nil {
		c.inlineMu.Unlock()
		return fmt.Errorf("persist selected inline result identity: %w", err)
	}

	state.admitted = true
	c.inlineMu.Unlock()

	if err := c.clearInlineReplyMarkup(ctx, result.InlineMessageID); err != nil && !isTelegramMessageNotModified(err) {
		logger.DebugCF("telegram", "Immediate inline button removal failed", map[string]any{
			"error": err.Error(),
		})
	}

	msg := buildTelegramInlineInboundMessage(result, sender)
	if guest {
		msg.Metadata[telegramGuestMetadataKey] = "true"
	}
	err := c.PublishInbound(ctx, msg)
	if err != nil {
		c.inlineMu.Lock()
		state.admitted = false
		c.inlineMu.Unlock()
	}
	return err
}

func renderTelegramInlineInitialContent(query, placeholder string, useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return utils.FormatQuotedMessage(query, placeholder)
	}

	escapedQuery := escapeHTML(
		strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(query, "\r\n", "\n"), "\r", "\n")),
	)
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
	if strings.HasPrefix(query.Data, telegramInlinePagePrefix) {
		page, err := strconv.Atoi(strings.TrimPrefix(query.Data, telegramInlinePagePrefix))
		if err == nil && query.InlineMessageID != "" {
			err = c.showInlinePage(ctx, query.InlineMessageID, query.From.ID, page)
		}
		text := ""
		if err != nil {
			text = "Unable to load this answer page. Please try again."
		}
		return c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text})
	}
	if query.Data != telegramInlineCallbackData {
		return nil
	}
	return c.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID))
}
