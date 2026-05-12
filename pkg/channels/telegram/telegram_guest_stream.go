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
)

const telegramGuestMessageLimit = 4096

func clampTelegramGuestContent(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= telegramGuestMessageLimit {
		return string(runes)
	}

	suffix := "\n\n..."
	limit := telegramGuestMessageLimit - len([]rune(suffix))
	if limit < 1 {
		limit = telegramGuestMessageLimit
		suffix = ""
	}
	return strings.TrimSpace(string(runes[:limit])) + suffix
}

func (c *TelegramChannel) editGuestInlineMessageText(ctx context.Context, inlineMessageID, content string) error {
	useMarkdownV2 := c.config.Channels.Telegram.UseMarkdownV2
	clamped := clampTelegramGuestContent(content)
	parsedContent := parseContent(clamped, useMarkdownV2)

	params := &telego.EditMessageTextParams{
		InlineMessageID: inlineMessageID,
		Text:            parsedContent,
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

type telegramGuestStreamer struct {
	channel          *TelegramChannel
	inlineMessageID  string
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	failed           bool
	mu               sync.Mutex
}

func (s *telegramGuestStreamer) Update(ctx context.Context, content string) error {
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

	if err := s.channel.editGuestInlineMessageText(ctx, s.inlineMessageID, content); err != nil {
		logger.WarnCF("telegram", "Guest edit failed during stream update, disabling streaming", map[string]any{
			"error": err.Error(),
		})
		s.failed = true
		return nil
	}

	s.lastLen = len(content)
	s.lastAt = now
	return nil
}

func (s *telegramGuestStreamer) Finalize(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.channel.editGuestInlineMessageText(ctx, s.inlineMessageID, content); err != nil {
		return fmt.Errorf("telegram guest finalize: %w", err)
	}
	return nil
}

func (s *telegramGuestStreamer) Cancel(_ context.Context) {}

func (c *TelegramChannel) beginGuestStream(chatID string) (channels.Streamer, error) {
	inlineMessageID, ok := telegramGuestInlineMessageID(chatID)
	if !ok {
		return nil, fmt.Errorf("invalid guest chat target")
	}

	streamCfg := c.config.Channels.Telegram.Streaming
	return &telegramGuestStreamer{
		channel:          c,
		inlineMessageID:  inlineMessageID,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}
