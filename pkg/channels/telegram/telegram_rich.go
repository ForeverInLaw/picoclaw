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
	for pos := 0; pos < len(content); {
		start := strings.IndexAny(content[pos:], "`~")
		if start < 0 {
			result.WriteString(normalizeFormulaDelimiters(content[pos:]))
			break
		}
		start += pos
		result.WriteString(normalizeFormulaDelimiters(content[pos:start]))
		n := markdownDelimiterLength(content, start)
		end := start + n
		if n >= 3 && markdownFencePosition(content, start) {
			end = markdownFenceEnd(content, start, n)
		} else if content[start] == '`' {
			for next := end; next < len(content); {
				match := strings.IndexByte(content[next:], '`')
				if match < 0 {
					break
				}
				match += next
				length := markdownDelimiterLength(content, match)
				if length == n {
					end = match + n
					break
				}
				next = match + length
			}
		}
		result.WriteString(content[start:end])
		pos = end
	}
	return result.String()
}

func markdownDelimiterLength(content string, start int) int {
	end := start + 1
	for end < len(content) && content[end] == content[start] {
		end++
	}
	return end - start
}

func markdownFencePosition(content string, start int) bool {
	lineStart := strings.LastIndexByte(content[:start], '\n') + 1
	indent := content[lineStart:start]
	return len(indent) <= 3 && strings.Trim(indent, " ") == ""
}

// Fences close only at a line's start, with the same character and at least
// the opening length. An unfinished block remains opaque during streaming.
func markdownFenceEnd(content string, start, length int) int {
	for pos := start + length; pos < len(content); {
		newline := strings.IndexByte(content[pos:], '\n')
		if newline < 0 {
			break
		}
		pos += newline + 1
		match := pos
		for match < len(content) && content[match] == ' ' {
			match++
		}
		if match < len(content) && content[match] == content[start] && markdownFencePosition(content, match) {
			n := markdownDelimiterLength(content, match)
			lineEnd := strings.IndexByte(content[match+n:], '\n')
			if lineEnd < 0 {
				lineEnd = len(content)
			} else {
				lineEnd += match + n
			}
			if n >= length && strings.TrimSpace(content[match+n:lineEnd]) == "" {
				return match + n
			}
		}
	}
	return len(content)
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
func (c *TelegramChannel) sendRichReply(
	ctx context.Context,
	chatID int64,
	threadID int,
	replyTo, content string,
) (int, error) {
	firstID, _, err := c.deliverReply(ctx, chatID, threadID, replyTo, content, 0)
	return firstID, err
}

func (c *TelegramChannel) sendPlainReply(
	ctx context.Context,
	chatID int64,
	threadID int,
	replyTo, content string,
) (*telego.Message, error) {
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
