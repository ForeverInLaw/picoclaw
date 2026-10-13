package telegram

import (
	"context"

	"github.com/mymmrac/telego"
)

const telegramInlineMessageLimit = 4096

func (c *TelegramChannel) clearInlineReplyMarkup(ctx context.Context, inlineMessageID string) error {
	// Omit reply_markup entirely to remove the inline keyboard.
	// Sending an empty keyboard object can be treated by Telegram as
	// "keep markup, but with zero rows" instead of clearing it.
	_, err := c.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		InlineMessageID: inlineMessageID,
	})
	return err
}

func (c *TelegramChannel) editInlineMessageText(ctx context.Context, inlineMessageID, content string) error {
	return c.sendInlineAnswer(ctx, inlineMessageID, content)
}

func telegramParseMode(useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return telego.ModeMarkdownV2
	}
	return telego.ModeHTML
}
