package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/channels"
)

const telegramRichMessageLimit = 32768

var (
	inlineFormula  = regexp.MustCompile(`(?s)\\\((.*?)\\\)`)
	displayFormula = regexp.MustCompile(`(?s)\\\[(.*?)\\\]`)
)

// Rich Markdown uses dollar delimiters for native formulas. Leave source code
// untouched, including unfinished code blocks in streamed previews.
func normalizeRichMarkdown(content string) string {
	var result strings.Builder
	for len(content) > 0 {
		start := strings.IndexAny(content, "`~")
		if start < 0 {
			result.WriteString(normalizeFormulaDelimiters(content))
			break
		}
		result.WriteString(normalizeFormulaDelimiters(content[:start]))
		content = content[start:]
		n := 1
		for n < len(content) && content[n] == content[0] {
			n++
		}
		if content[0] == '~' && n < 3 {
			result.WriteString(content[:n])
			content = content[n:]
			continue
		}
		delimiter := content[:n]
		end := strings.Index(content[n:], delimiter)
		if end < 0 {
			result.WriteString(content)
			break
		}
		end += 2 * n
		result.WriteString(content[:end])
		content = content[end:]
	}
	return result.String()
}

func normalizeFormulaDelimiters(content string) string {
	content = displayFormula.ReplaceAllStringFunc(content, func(formula string) string {
		return "$$" + formula[2:len(formula)-2] + "$$"
	})
	return inlineFormula.ReplaceAllStringFunc(content, func(formula string) string {
		return "$" + formula[2:len(formula)-2] + "$"
	})
}

func isTelegramFormattingError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"can't parse", "cannot parse", "invalid rich", "rich message", "too many blocks", "too many nested", "message is too long"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func telegramDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiError *ta.Error
	if errors.As(err, &apiError) && apiError.ErrorCode >= 400 && apiError.ErrorCode < 500 && apiError.ErrorCode != 429 {
		return fmt.Errorf("telegram delivery: %w: %w", channels.ErrSendFailed, err)
	}
	return fmt.Errorf("telegram delivery: %w: %w", channels.ErrTemporary, err)
}

func replyParameters(replyTo string) *telego.ReplyParameters {
	if messageID, err := strconv.Atoi(replyTo); err == nil && messageID != 0 {
		return &telego.ReplyParameters{MessageID: messageID}
	}
	return nil
}

// sendRichReply keeps rich content intact up to Telegram's native rich limit,
// and delivers every remaining chunk. Formatting rejection falls back to plain
// Markdown, without changing the reply or topic destination.
func (c *TelegramChannel) sendRichReply(ctx context.Context, chatID int64, threadID int, replyTo, content string) (int, error) {
	if strings.TrimSpace(content) == "" {
		return 0, nil
	}
	var firstID int
	for _, chunk := range channels.SplitMessage(normalizeRichMarkdown(content), telegramRichMessageLimit) {
		message, err := c.bot.SendRichMessage(ctx, &telego.SendRichMessageParams{
			ChatID: tu.ID(chatID), MessageThreadID: threadID,
			ReplyParameters: replyParameters(replyTo), RichMessage: tu.RichMessageMarkdown(chunk),
		})
		if isTelegramFormattingError(err) {
			message, err = c.sendPlainReply(ctx, chatID, threadID, replyTo, chunk)
		}
		if err != nil {
			return firstID, telegramDeliveryError(err)
		}
		if firstID == 0 && message != nil {
			firstID = message.MessageID
		}
		replyTo = ""
	}
	return firstID, nil
}

func (c *TelegramChannel) sendPlainReply(ctx context.Context, chatID int64, threadID int, replyTo, content string) (*telego.Message, error) {
	var first *telego.Message
	for _, chunk := range channels.SplitMessage(content, telegramInlineMessageLimit) {
		params := tu.Message(tu.ID(chatID), chunk)
		params.MessageThreadID = threadID
		params.ReplyParameters = replyParameters(replyTo)
		message, err := c.bot.SendMessage(ctx, params)
		if err != nil {
			return first, err
		}
		if first == nil {
			first = message
		}
		replyTo = ""
	}
	return first, nil
}

func (c *TelegramChannel) editRichReply(ctx context.Context, chatID int64, threadID, messageID int, content string) error {
	chunks := channels.SplitMessage(normalizeRichMarkdown(content), telegramRichMessageLimit)
	if len(chunks) == 0 {
		return nil
	}
	rich := tu.RichMessageMarkdown(chunks[0])
	_, err := c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID: tu.ID(chatID), MessageID: messageID, RichMessage: &rich,
	})
	if isTelegramFormattingError(err) {
		plainChunks := channels.SplitMessage(chunks[0], telegramInlineMessageLimit)
		_, err = c.bot.EditMessageText(ctx, tu.EditMessageText(tu.ID(chatID), messageID, plainChunks[0]))
		if err == nil || isTelegramMessageNotModified(err) {
			for _, chunk := range plainChunks[1:] {
				if _, err = c.sendPlainReply(ctx, chatID, threadID, "", chunk); err != nil {
					break
				}
			}
		}
	}
	if err != nil && !isTelegramMessageNotModified(err) {
		return telegramDeliveryError(err)
	}
	for _, chunk := range chunks[1:] {
		if _, err = c.sendRichReply(ctx, chatID, threadID, "", chunk); err != nil {
			return err
		}
	}
	return nil
}
