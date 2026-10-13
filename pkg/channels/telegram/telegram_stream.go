package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// BeginStream uses ephemeral rich drafts in private chats. Group messages and
// inline destinations use rich message edits, supported by those destinations.
func (c *TelegramChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if !c.config.Channels.Telegram.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := c.config.Channels.Telegram.Streaming
	stream := &telegramStreamer{
		channel:          c,
		replyTo:          bus.StreamReplyTo(ctx),
		draftID:          cryptoRandInt(),
		throttleInterval: time.Duration(cfg.ThrottleSeconds) * time.Second,
		minGrowth:        cfg.MinGrowthChars,
	}
	if inlineID, ok := telegramInlineMessageID(chatID); ok {
		stream.inlineMessageID = inlineID
		return stream, nil
	}
	var err error
	stream.chatID, stream.threadID, err = parseTelegramChatID(chatID)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

type telegramStreamer struct {
	channel          *TelegramChannel
	chatID           int64
	threadID         int
	replyTo          string
	inlineMessageID  string
	draftID          int
	previewMessageID int
	delivery         *telegramReplyDelivery
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	previewFailed    bool
	finalized        bool
	canceled         bool
	mu               sync.Mutex
}

func (s *telegramStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.previewFailed || s.finalized || s.canceled || strings.TrimSpace(content) == "" {
		return nil
	}
	now := time.Now()
	if s.lastLen > 0 && now.Sub(s.lastAt) < s.throttleInterval &&
		utf8.RuneCountInString(content)-s.lastLen < s.minGrowth {
		return nil
	}
	preview := richAnswerPreview(content, telegramRichMessageLimit)
	var err error
	switch {
	case s.inlineMessageID != "":
		err = s.editPreview(ctx, 0, s.inlineMessageID, preview)
	case s.chatID > 0:
		err = s.channel.bot.SendRichMessageDraft(
			ctx,
			&telego.SendRichMessageDraftParams{
				ChatID:          s.chatID,
				MessageThreadID: s.threadID,
				DraftID:         s.draftID,
				RichMessage:     tu.RichMessageMarkdown(preview),
			},
		)
	case s.previewMessageID == 0:
		params := &telego.SendRichMessageParams{
			ChatID:          tu.ID(s.chatID),
			MessageThreadID: s.threadID,
			ReplyParameters: replyParameters(s.replyTo),
			RichMessage:     tu.RichMessageMarkdown(preview),
		}
		var message *telego.Message
		message, err = s.channel.bot.SendRichMessage(ctx, params)
		if isTelegramFormattingError(err) {
			plain := tu.Message(tu.ID(s.chatID), richAnswerPreview(content, telegramInlineMessageLimit))
			plain.MessageThreadID = s.threadID
			plain.ReplyParameters = params.ReplyParameters
			message, err = s.channel.bot.SendMessage(ctx, plain)
		}
		if err == nil && message != nil {
			s.previewMessageID = message.MessageID
		}
	default:
		err = s.editPreview(ctx, s.previewMessageID, "", preview)
	}
	if err != nil {
		// Preview is best effort. Preserve its identity and retry complete final
		// delivery separately; never restart the model or replay a tool.
		logger.WarnCF(
			"telegram",
			"Rich answer preview failed; retaining final delivery",
			map[string]any{"error": err.Error()},
		)
		s.previewFailed = true
		return nil
	}
	s.lastLen = utf8.RuneCountInString(content)
	s.lastAt = now
	return nil
}

func (s *telegramStreamer) editPreview(ctx context.Context, messageID int, inlineID, content string) error {
	rich := tu.RichMessageMarkdown(content)
	params := &telego.EditMessageTextParams{MessageID: messageID, InlineMessageID: inlineID, RichMessage: &rich}
	if inlineID == "" {
		params.ChatID = tu.ID(s.chatID)
	} else {
		s.channel.inlineMu.Lock()
		defer s.channel.inlineMu.Unlock()
		state := s.channel.inlineReplyLocked(inlineID)
		if state.finalized {
			return nil
		}
		params.ReplyMarkup = inlineStatusMarkup(state.markup, state.retryText)
	}
	_, err := s.channel.bot.EditMessageText(ctx, params)
	if isTelegramFormattingError(err) {
		params.RichMessage = nil
		params.Text = richAnswerPreview(content, telegramInlineMessageLimit)
		_, err = s.channel.bot.EditMessageText(ctx, params)
	}
	if isTelegramMessageNotModified(err) {
		return nil
	}
	return telegramDeliveryError(err)
}

func (s *telegramStreamer) Finalize(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.canceled {
		return context.Canceled
	}
	if s.finalized {
		return nil
	}
	s.channel.deliveryMu.Lock()
	completed := s.delivery != nil && s.delivery.next == len(s.delivery.parts)
	terminalErr := error(nil)
	if s.delivery != nil {
		terminalErr = s.delivery.terminalErr
	}
	s.channel.deliveryMu.Unlock()
	if completed {
		s.finalized = true
		return nil
	}
	if terminalErr != nil {
		return terminalErr
	}
	var err error
	switch {
	case s.inlineMessageID != "":
		err = safeFinalEdit(ctx, func() error { return s.channel.sendInlineAnswer(ctx, s.inlineMessageID, content) })
	case s.previewMessageID != 0:
		_, s.delivery, err = s.channel.deliverReply(ctx, s.chatID, s.threadID, s.replyTo, content, s.previewMessageID)
	default:
		_, s.delivery, err = s.channel.deliverReply(ctx, s.chatID, s.threadID, s.replyTo, content, 0)
	}
	if err != nil {
		return fmt.Errorf("telegram finalize: %w", err)
	}
	s.finalized = true
	return nil
}

// Edits have a stable message identity and are safe to retry once. New message
// sends use the channel manager's delivery policy instead of a nested retry loop.
func safeFinalEdit(ctx context.Context, edit func() error) error {
	err := edit()
	var apiError *ta.Error
	if errors.As(err, &apiError) && apiError.ErrorCode == 429 {
		return err
	}
	if errors.Is(err, channels.ErrTemporary) && ctx.Err() == nil {
		return edit()
	}
	return err
}

func richAnswerPreview(content string, limit int) string {
	content = normalizeRichMarkdown(content)
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	const suffix = "\n\n(Preview; the complete answer follows.)"
	return string(runes[:limit-len([]rune(suffix))]) + suffix
}

func (s *telegramStreamer) Cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized || s.canceled {
		return
	}
	s.canceled = true
	if s.previewMessageID != 0 {
		_ = s.channel.bot.DeleteMessage(
			ctx,
			&telego.DeleteMessageParams{ChatID: tu.ID(s.chatID), MessageID: s.previewMessageID},
		)
	}
	// Private drafts expire automatically. Inline destinations retain their
	// selected placeholder/last preview; cancellation stops further edits.
}
