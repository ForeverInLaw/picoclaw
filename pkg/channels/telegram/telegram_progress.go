package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

type telegramActivity struct {
	messageID int
	content   [32]byte
	terminal  bool
	updatedAt time.Time
}

var _ channels.ProgressCapable = (*TelegramChannel)(nil)

// SendProgress tracks a separate message for every invocation, including delivery
// retries. It never edits the answer placeholder or calls the assistant again.
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
	chatID, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return err
	}
	progress := msg.Progress
	key := msg.ChatID + ":" + progress.RequestID + ":" + progress.ActionID
	rich, plain := renderTelegramActivity(msg)
	encoded, err := json.Marshal(rich)
	if err != nil {
		return err
	}
	content := sha256.Sum256(encoded)

	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	if c.activities == nil {
		c.activities = make(map[string]*telegramActivity)
	}
	c.pruneActivities()
	entry := c.activities[key]
	if entry != nil && (entry.content == content || entry.terminal && progress.State == bus.ProgressStarted) {
		return nil
	}
	if entry == nil {
		message, sendErr := c.bot.SendRichMessage(ctx, &telego.SendRichMessageParams{
			ChatID: tu.ID(chatID), MessageThreadID: threadID, ReplyParameters: replyParameters(msg.ReplyToMessageID), RichMessage: rich,
		})
		if isTelegramFormattingError(sendErr) {
			message, sendErr = c.sendPlainReply(ctx, chatID, threadID, msg.ReplyToMessageID, limitActivityText(plain, telegramInlineMessageLimit))
		}
		if sendErr != nil {
			return telegramDeliveryError(sendErr)
		}
		if message == nil {
			return fmt.Errorf("telegram activity missing message identity: %w", channels.ErrTemporary)
		}
		entry = &telegramActivity{messageID: message.MessageID}
		c.activities[key] = entry
	} else {
		_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: tu.ID(chatID), MessageID: entry.messageID, RichMessage: &rich})
		if isTelegramFormattingError(err) {
			_, err = c.bot.EditMessageText(ctx, tu.EditMessageText(tu.ID(chatID), entry.messageID, limitActivityText(plain, telegramInlineMessageLimit)))
		}
		if err != nil && !isTelegramMessageNotModified(err) {
			return telegramDeliveryError(err)
		}
	}
	entry.content = content
	entry.terminal = progress.State == bus.ProgressCompleted || progress.State == bus.ProgressFailed
	entry.updatedAt = time.Now()
	return nil
}

func (c *TelegramChannel) pruneActivities() {
	now := time.Now()
	for key, entry := range c.activities {
		if now.Sub(entry.updatedAt) > 24*time.Hour {
			delete(c.activities, key)
		}
	}
	// Keep recent completed invocation identities so retried completion delivery
	// edits the existing message, while bounding state for long-running bots.
	for len(c.activities) >= 2048 {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range c.activities {
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.updatedAt
			}
		}
		delete(c.activities, oldestKey)
	}
}

func renderTelegramActivity(msg bus.OutboundMessage) (telego.InputRichMessage, string) {
	progress := msg.Progress
	if progress.Kind != bus.ProgressTool {
		text := limitActivityText(strings.TrimSpace(msg.Content), 2000)
		return tu.RichMessage(tu.RichBlockParagraph(tu.RichTextPlain(text))), text
	}
	status := "Running"
	if progress.State == bus.ProgressCompleted {
		status = "Done"
	}
	if progress.State == bus.ProgressFailed {
		status = "Failed"
	}
	title := limitActivityText(progress.Tool, 128) + " · " + status
	preview := toolActionPreview(progress.Arguments)
	blocks := []telego.InputRichBlock{tu.RichBlockParagraph(tu.RichTextBold(tu.RichTextPlain(title)))}
	plain := title
	if preview != "" {
		blocks = append(blocks, tu.RichBlockPreformatted(tu.RichTextPlain(preview)))
		plain += "\n" + preview
	}
	output := strings.TrimSpace(progress.Result)
	if progress.Error != "" {
		output = strings.TrimSpace(progress.Error)
	}
	if output != "" {
		output = limitActivityText(output, 28000)
		plain += "\n\n" + output
		if len([]rune(output)) > 600 || strings.Count(output, "\n") > 6 {
			summary := limitActivityText(strings.SplitN(output, "\n", 2)[0], 200)
			blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextPlain(summary)), tu.RichBlockDetails(tu.RichTextPlain("Tool output"), tu.RichBlockPreformatted(tu.RichTextPlain(output))))
		} else {
			blocks = append(blocks, tu.RichBlockPreformatted(tu.RichTextPlain(output)))
		}
	}
	return tu.RichMessage(blocks...), plain
}

func toolActionPreview(arguments map[string]any) string {
	for _, key := range []string{"command", "path", "query", "url", "file_path", "pattern"} {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return limitActivityText(value, 1200)
		}
	}
	if len(arguments) == 0 {
		return ""
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return ""
	}
	return limitActivityText(string(data), 1200)
}

func limitActivityText(content string, limit int) string {
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	const suffix = "\n[truncated]"
	return string(runes[:limit-len([]rune(suffix))]) + suffix
}
