package telegram

import (
	"context"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// Retry status lives in reply markup, outside the answer body. Final delivery
// and queued status edits share the same lock, so a late status cannot replace
// pagination or reappear on a completed answer.
type telegramInlineReplyState struct {
	markup    *telego.InlineKeyboardMarkup
	retryID   string
	retryText string
	finalized bool
	updatedAt time.Time
}

func (c *TelegramChannel) inlineReplyLocked(inlineID string) *telegramInlineReplyState {
	if c.inlineReplies == nil {
		c.inlineReplies = make(map[string]*telegramInlineReplyState)
	}
	if state := c.inlineReplies[inlineID]; state != nil {
		state.updatedAt = time.Now()
		return state
	}
	for id, state := range c.inlineReplies {
		if time.Since(state.updatedAt) > 24*time.Hour {
			delete(c.inlineReplies, id)
		}
	}
	if len(c.inlineReplies) >= 2048 {
		oldestID := ""
		var oldest time.Time
		for id, state := range c.inlineReplies {
			if oldestID == "" || state.updatedAt.Before(oldest) {
				oldestID = id
				oldest = state.updatedAt
			}
		}
		delete(c.inlineReplies, oldestID)
	}
	state := &telegramInlineReplyState{updatedAt: time.Now()}
	c.inlineReplies[inlineID] = state
	return state
}

func inlineStatusMarkup(markup *telego.InlineKeyboardMarkup, status string) *telego.InlineKeyboardMarkup {
	rows := [][]telego.InlineKeyboardButton{}
	if markup != nil {
		rows = append(rows, markup.InlineKeyboard...)
	}
	if status != "" {
		rows = append(rows, []telego.InlineKeyboardButton{{Text: status, Disabled: &telego.DisabledButton{}}})
	}
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (c *TelegramChannel) sendInlineRetryStatus(ctx context.Context, inlineID string, msg bus.OutboundMessage) error {
	c.inlineMu.Lock()
	defer c.inlineMu.Unlock()
	state := c.inlineReplyLocked(inlineID)
	if state.finalized {
		return nil
	}
	retryID := msg.Progress.RequestID + ":" + msg.Progress.ActionID
	text := ""
	if msg.Progress.State == bus.ProgressStarted {
		text = strings.TrimSpace(msg.Content)
		if text == "" {
			text = "Retrying request…"
		}
		text = limitActivityText(text, 96)
	} else if state.retryID != retryID {
		return nil
	}
	if state.retryText == text {
		state.retryID = retryID
		return nil
	}
	_, err := c.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		InlineMessageID: inlineID, ReplyMarkup: inlineStatusMarkup(state.markup, text),
	})
	if err != nil && !isTelegramMessageNotModified(err) {
		return telegramDeliveryError(err)
	}
	state.retryID = retryID
	state.retryText = text
	return nil
}
