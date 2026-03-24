package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const telegramInlineMessageLimit = 4096

func clampTelegramInlineContent(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= telegramInlineMessageLimit {
		return string(runes)
	}

	suffix := "\n\n..."
	limit := telegramInlineMessageLimit - len([]rune(suffix))
	if limit < 1 {
		limit = telegramInlineMessageLimit
		suffix = ""
	}
	return strings.TrimSpace(string(runes[:limit])) + suffix
}

func (c *TelegramChannel) clearInlineReplyMarkup(ctx context.Context, inlineMessageID string) error {
	_, err := c.bot.EditMessageReplyMarkup(ctx, (&telego.EditMessageReplyMarkupParams{
		InlineMessageID: inlineMessageID,
	}).WithReplyMarkup(&telego.InlineKeyboardMarkup{}))
	return err
}

func (c *TelegramChannel) editInlineMessageText(ctx context.Context, inlineMessageID, content string) error {
	useMarkdownV2 := c.config.Channels.Telegram.UseMarkdownV2
	clamped := c.renderInlineMessageContent(inlineMessageID, content)

	params := &telego.EditMessageTextParams{
		InlineMessageID: inlineMessageID,
		Text:            clamped,
	}
	params.ParseMode = telegramParseMode(useMarkdownV2)

	_, err := c.bot.EditMessageText(ctx, params)
	if isTelegramMessageNotModified(err) {
		return nil
	}
	if err != nil {
		logParseFailed(err, useMarkdownV2)
		_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			InlineMessageID: inlineMessageID,
			Text:            clamped,
			ParseMode:       telegramParseMode(useMarkdownV2),
		})
		if isTelegramMessageNotModified(err) {
			return nil
		}
	}
	return err
}

func telegramParseMode(useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return telego.ModeMarkdownV2
	}
	return telego.ModeHTML
}

func renderTelegramInlineQuotedBody(query, body string, useMarkdownV2 bool) string {
	body = strings.TrimSpace(body)
	query = strings.TrimSpace(query)
	if query == "" {
		if useMarkdownV2 {
			return markdownToTelegramMarkdownV2(body)
		}
		return markdownToTelegramHTML(body)
	}

	if useMarkdownV2 {
		return utils.FormatQuotedMessage(query, markdownToTelegramMarkdownV2(body))
	}

	escapedQuery := escapeHTML(strings.ReplaceAll(strings.ReplaceAll(query, "\r\n", "\n"), "\r", "\n"))
	escapedQuery = strings.ReplaceAll(escapedQuery, "\n", "<br>")
	parsedBody := markdownToTelegramHTML(body)
	if parsedBody == "" {
		return "<blockquote>" + escapedQuery + "</blockquote>"
	}
	return "<blockquote>" + escapedQuery + "</blockquote>\n\n" + parsedBody
}

func (c *TelegramChannel) rememberInlineQuery(inlineMessageID, query string) {
	inlineMessageID = strings.TrimSpace(inlineMessageID)
	query = strings.TrimSpace(query)
	if inlineMessageID == "" || query == "" {
		return
	}
	c.inlineMu.Lock()
	c.inlineQueries[inlineMessageID] = query
	c.inlineMu.Unlock()
}

func (c *TelegramChannel) inlineQuery(inlineMessageID string) string {
	inlineMessageID = strings.TrimSpace(inlineMessageID)
	if inlineMessageID == "" {
		return ""
	}
	c.inlineMu.RLock()
	query := c.inlineQueries[inlineMessageID]
	c.inlineMu.RUnlock()
	return strings.TrimSpace(query)
}

func (c *TelegramChannel) renderInlineMessageContent(inlineMessageID, body string) string {
	return clampTelegramInlineContent(
		renderTelegramInlineQuotedBody(
			c.inlineQuery(inlineMessageID),
			body,
			c.config.Channels.Telegram.UseMarkdownV2,
		),
	)
}

type telegramInlineStreamer struct {
	channel          *TelegramChannel
	inlineMessageID  string
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	failed           bool
	keyboardCleared  bool
	mu               sync.Mutex
}

func (s *telegramInlineStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failed {
		return nil
	}

	now := time.Now()
	growth := len(content) - s.lastLen
	if s.lastLen > 0 && now.Sub(s.lastAt) < s.throttleInterval && growth < s.minGrowth {
		return nil
	}

	if err := s.channel.editInlineMessageText(ctx, s.inlineMessageID, content); err != nil {
		logger.WarnCF("telegram", "Inline edit failed during stream update, disabling streaming", map[string]any{
			"error": err.Error(),
		})
		s.failed = true
		return nil
	}
	if !s.keyboardCleared {
		if err := s.channel.clearInlineReplyMarkup(ctx, s.inlineMessageID); err == nil {
			s.keyboardCleared = true
		}
	}

	s.lastLen = len(content)
	s.lastAt = now
	return nil
}

func (s *telegramInlineStreamer) Finalize(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.channel.editInlineMessageText(ctx, s.inlineMessageID, content); err != nil {
		return fmt.Errorf("telegram inline finalize: %w", err)
	}
	if !s.keyboardCleared {
		if err := s.channel.clearInlineReplyMarkup(ctx, s.inlineMessageID); err != nil {
			logger.WarnCF("telegram", "Failed to clear inline keyboard after finalize", map[string]any{
				"error": err.Error(),
			})
		} else {
			s.keyboardCleared = true
		}
	}
	return nil
}

func (s *telegramInlineStreamer) Cancel(ctx context.Context) {
	if s == nil {
		return
	}
}

func (c *TelegramChannel) beginInlineStream(chatID string) (channels.Streamer, error) {
	inlineMessageID, ok := telegramInlineMessageID(chatID)
	if !ok {
		return nil, fmt.Errorf("invalid inline chat target")
	}

	streamCfg := c.config.Channels.Telegram.Streaming
	return &telegramInlineStreamer{
		channel:          c,
		inlineMessageID:  inlineMessageID,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}
