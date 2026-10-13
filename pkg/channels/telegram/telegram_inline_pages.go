package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/channels"
)

const telegramInlinePagePrefix = "__picoclaw_inline_page__:"

// Published answers are transport state, separate from conversation history.
// Store only paginated results so their navigation survives bot restarts.
type telegramInlinePages struct {
	Content string `json:"content"`
	Plain   bool   `json:"plain,omitempty"`
}

func (c *TelegramChannel) inlinePagesPath(inlineMessageID string) string {
	botID := sha256.Sum256([]byte(strings.SplitN(c.config.Channels.Telegram.Token, ":", 2)[0]))
	messageID := sha256.Sum256([]byte(inlineMessageID))
	return filepath.Join(c.config.WorkspacePath(), ".state", "telegram-inline", hex.EncodeToString(botID[:8]), hex.EncodeToString(messageID[:])+".json")
}

func (c *TelegramChannel) saveInlinePages(inlineMessageID string, pages telegramInlinePages) error {
	path := c.inlinePagesPath(inlineMessageID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pages-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (c *TelegramChannel) showInlinePage(ctx context.Context, inlineMessageID string, page int) error {
	data, err := os.ReadFile(c.inlinePagesPath(inlineMessageID))
	if err != nil {
		return err
	}
	var pages telegramInlinePages
	if err = json.Unmarshal(data, &pages); err != nil {
		return err
	}
	return c.editInlinePage(ctx, inlineMessageID, pages, page)
}

func inlinePageContent(pages telegramInlinePages, page int) (string, *telego.InlineKeyboardMarkup, error) {
	limit := telegramRichMessageLimit
	if pages.Plain {
		limit = telegramInlineMessageLimit
	}
	chunks := channels.SplitMessage(pages.Content, limit-64)
	if page < 0 || page >= len(chunks) {
		return "", nil, fmt.Errorf("inline answer page unavailable")
	}
	if len(chunks) == 1 {
		return chunks[0], nil, nil
	}
	buttons := []telego.InlineKeyboardButton{}
	if page > 0 {
		buttons = append(buttons, telego.InlineKeyboardButton{Text: "← Previous", CallbackData: telegramInlinePagePrefix + strconv.Itoa(page-1)})
	}
	if page+1 < len(chunks) {
		buttons = append(buttons, telego.InlineKeyboardButton{Text: "Next →", CallbackData: telegramInlinePagePrefix + strconv.Itoa(page+1)})
	}
	return chunks[page] + fmt.Sprintf("\n\nPage %d/%d", page+1, len(chunks)), &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{buttons}}, nil
}

func (c *TelegramChannel) editInlinePage(ctx context.Context, inlineMessageID string, pages telegramInlinePages, page int) error {
	content, markup, err := inlinePageContent(pages, page)
	if err != nil {
		return err
	}
	if markup == nil {
		markup = &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
	}
	params := &telego.EditMessageTextParams{InlineMessageID: inlineMessageID, ReplyMarkup: markup}
	if pages.Plain {
		params.Text = content
	} else {
		rich := tu.RichMessageMarkdown(content)
		params.RichMessage = &rich
	}
	_, err = c.bot.EditMessageText(ctx, params)
	if isTelegramFormattingError(err) && !pages.Plain {
		// Formatting rejection can occur below the character limit (e.g. too
		// many blocks). Repage the full source using Telegram's plain limit.
		pages.Plain = true
		if len([]rune(pages.Content)) > telegramInlineMessageLimit-64 {
			if err = c.saveInlinePages(inlineMessageID, pages); err != nil {
				return err
			}
		}
		return c.editInlinePage(ctx, inlineMessageID, pages, 0)
	}
	if isTelegramMessageNotModified(err) {
		return nil
	}
	return telegramDeliveryError(err)
}

func (c *TelegramChannel) sendInlineAnswer(ctx context.Context, inlineMessageID, content string) error {
	pages := telegramInlinePages{Content: normalizeRichMarkdown(content)}
	if strings.TrimSpace(pages.Content) == "" {
		return nil
	}
	if len([]rune(pages.Content)) > telegramRichMessageLimit-64 {
		if err := c.saveInlinePages(inlineMessageID, pages); err != nil {
			return fmt.Errorf("save complete inline answer: %w", err)
		}
	}
	return c.editInlinePage(ctx, inlineMessageID, pages, 0)
}
