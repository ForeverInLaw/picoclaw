package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

const telegramProgressInterval = 1500 * time.Millisecond

type telegramActivity struct {
	chatID, replyTo string
	messageID       int
	content         [32]byte
	terminal        bool
	updatedAt       time.Time
	sentAt          time.Time
	retryAt         time.Time
	tool            bool
	rows            []telegramActivityRow
	actions         map[string]bool
	actionOrder     []string
	timer           *time.Timer
}

var _ channels.ProgressCapable = (*TelegramChannel)(nil)

// SendProgress groups consecutive tools in a quiet message, separate from the answer.
// Only starts are displayed; results and reasoning remain in their owning paths.
func (c *TelegramChannel) SendProgress(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	if msg.Progress == nil {
		return fmt.Errorf("telegram progress payload missing: %w", channels.ErrSendFailed)
	}
	if inlineID, ok := telegramInlineMessageID(msg.ChatID); ok {
		if msg.Progress.Kind == bus.ProgressRetry {
			return c.sendInlineRetryStatus(ctx, inlineID, msg)
		}
		return nil
	}
	progress := msg.Progress
	isTool := progress.Kind == bus.ProgressTool
	if isTool && progress.State != bus.ProgressStarted {
		return nil
	}
	key := msg.ChatID + ":" + progress.RequestID + ":" + string(progress.Kind)
	if !isTool {
		key += ":" + progress.ActionID
	}
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	if c.activities == nil {
		c.activities = make(map[string]*telegramActivity)
	}
	c.pruneActivities()
	entry := c.activities[key]
	if entry == nil {
		entry = &telegramActivity{
			chatID:  msg.ChatID,
			replyTo: msg.ReplyToMessageID,
			tool:    isTool,
			actions: make(map[string]bool),
		}
		c.activities[key] = entry
	}
	if entry.terminal && progress.State == bus.ProgressStarted {
		return nil
	}
	entry.updatedAt = time.Now()
	if isTool {
		if !entry.actions[progress.ActionID] {
			row := toolActivityRow(progress)
			rows := append([]telegramActivityRow(nil), entry.rows...)
			rows = appendActivityRow(rows, row)
			_, plain := renderActivityRows(rows)
			if len(utf16.Encode([]rune(plain))) > 4000 {
				if err := c.flushActivity(ctx, entry); err != nil {
					return err
				}
				entry.resetBubble()
			}
			entry.rows = appendActivityRow(entry.rows, row)
			entry.actions[progress.ActionID] = true
			entry.actionOrder = append(entry.actionOrder, progress.ActionID)
			if len(entry.actionOrder) > 2048 {
				delete(entry.actions, entry.actionOrder[0])
				entry.actionOrder = entry.actionOrder[1:]
			}
		}
		if entry.messageID != 0 && time.Since(entry.sentAt) < telegramProgressInterval {
			c.scheduleActivity(entry, telegramProgressInterval-time.Since(entry.sentAt))
			return nil
		}
	} else {
		entry.rows = []telegramActivityRow{{text: limitActivityText(strings.TrimSpace(msg.Content), 2000), count: 1}}
	}
	err := c.flushActivity(ctx, entry)
	if !isTool && err == nil {
		entry.terminal = progress.State != bus.ProgressStarted
	}
	return err
}

func (c *TelegramChannel) scheduleActivity(entry *telegramActivity, delay time.Duration) {
	if entry.timer != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		c.progressMu.Lock()
		defer c.progressMu.Unlock()
		if entry.timer != timer {
			return
		}
		entry.timer = nil
		if !c.IsRunning() || entry.terminal {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.flushActivity(ctx, entry)
	})
	entry.timer = timer
}

func (entry *telegramActivity) resetBubble() {
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	entry.messageID, entry.rows, entry.content = 0, nil, [32]byte{}
}

// Flush accepted starts before a text boundary. Later tools begin below that text.
func (c *TelegramChannel) finishActivities(ctx context.Context, chatID, replyTo string, terminal bool) {
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	for _, entry := range c.activities {
		if !entry.tool || entry.chatID != chatID || entry.replyTo != replyTo || entry.terminal {
			continue
		}
		_ = c.flushActivity(ctx, entry)
		entry.resetBubble()
		entry.terminal = terminal
	}
}

func (c *TelegramChannel) flushActivity(ctx context.Context, entry *telegramActivity) (err error) {
	if len(entry.rows) == 0 {
		return nil
	}
	rich, plain := renderActivityRows(entry.rows)
	encoded, err := json.Marshal(rich)
	if err != nil {
		return err
	}
	content := sha256.Sum256(encoded)
	if content == entry.content {
		return nil
	}
	if time.Now().Before(entry.retryAt) {
		return channels.ErrTemporary
	}
	chatID, threadID, err := parseTelegramChatID(entry.chatID)
	if err != nil {
		return err
	}
	defer func() {
		if !errors.Is(err, channels.ErrTemporary) || entry.messageID == 0 {
			return
		}
		delay := telegramProgressInterval
		var apiError *ta.Error
		if errors.As(err, &apiError) && apiError.Parameters != nil {
			delay = max(delay, time.Duration(apiError.Parameters.RetryAfter)*time.Second)
			entry.retryAt = time.Now().Add(delay)
		}
		entry.sentAt = time.Now().Add(delay - telegramProgressInterval)
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		c.scheduleActivity(entry, delay)
	}()
	if entry.messageID != 0 {
		_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID: tu.ID(chatID), MessageID: entry.messageID, RichMessage: &rich,
		})
		if isTelegramFormattingError(err) {
			_, err = c.bot.EditMessageText(ctx, tu.EditMessageText(tu.ID(chatID), entry.messageID, plain))
		}
		if err != nil && !isTelegramMessageNotModified(err) {
			var apiError *ta.Error
			if !errors.As(err, &apiError) || apiError.ErrorCode != 400 ||
				(!strings.Contains(apiError.Description, "message to edit not found") &&
					!strings.Contains(apiError.Description, "message can't be edited")) {
				return telegramDeliveryError(err)
			}
			entry.messageID = 0 // Deleted/expired messages need a fresh quiet bubble.
		}
	}
	if entry.messageID == 0 {
		message, sendErr := c.bot.SendRichMessage(ctx, &telego.SendRichMessageParams{
			ChatID: tu.ID(chatID), MessageThreadID: threadID, ReplyParameters: replyParameters(entry.replyTo),
			RichMessage: rich, DisableNotification: true,
		})
		if isTelegramFormattingError(sendErr) {
			params := tu.Message(tu.ID(chatID), plain)
			params.MessageThreadID, params.ReplyParameters, params.DisableNotification = threadID, replyParameters(
				entry.replyTo,
			), true
			message, sendErr = c.bot.SendMessage(ctx, params)
		}
		if sendErr != nil {
			return telegramDeliveryError(sendErr)
		}
		if message == nil {
			return fmt.Errorf("telegram activity missing message identity: %w", channels.ErrTemporary)
		}
		entry.messageID = message.MessageID
	}
	entry.content, entry.sentAt, entry.retryAt = content, time.Now(), time.Time{}
	return nil
}

func (c *TelegramChannel) pruneActivities() {
	now := time.Now()
	for key, entry := range c.activities {
		if now.Sub(entry.updatedAt) > 24*time.Hour {
			entry.resetBubble()
			delete(c.activities, key)
		}
	}
	for len(c.activities) >= 2048 {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range c.activities {
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey, oldest = key, entry.updatedAt
			}
		}
		c.activities[oldestKey].resetBubble()
		delete(c.activities, oldestKey)
	}
}

func limitActivityText(content string, limit int) string {
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit-3]) + "..."
}
