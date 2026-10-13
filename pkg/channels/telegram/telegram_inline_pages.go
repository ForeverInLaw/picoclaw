package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const telegramInlinePagePrefix = "__picoclaw_inline_page__:"

// Published answers are transport state, separate from conversation history.
// Selected result ownership is recorded before generation. Paginated answers
// retain their complete source so navigation survives bot restarts.
type telegramInlinePages struct {
	Content string `json:"content"`
	Plain   bool   `json:"plain,omitempty"`
	OwnerID int64  `json:"owner_id"`
}

const inlinePagesLifetime = 30 * 24 * time.Hour

func (c *TelegramChannel) inlinePagesPath(inlineMessageID string) string {
	botID := sha256.Sum256([]byte(strings.SplitN(c.config.Channels.Telegram.Token, ":", 2)[0]))
	messageID := sha256.Sum256([]byte(inlineMessageID))
	return filepath.Join(
		c.config.WorkspacePath(),
		".state",
		"telegram-inline",
		hex.EncodeToString(botID[:8]),
		hex.EncodeToString(messageID[:])+".json",
	)
}

func (c *TelegramChannel) saveInlinePages(inlineMessageID string, pages telegramInlinePages) error {
	path := c.inlinePagesPath(inlineMessageID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if pages.OwnerID == 0 {
		previous, err := c.loadInlinePages(inlineMessageID)
		if err != nil {
			return fmt.Errorf("inline result owner unavailable: %w", err)
		}
		pages.OwnerID = previous.OwnerID
	}
	data, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	if len(data) > 64<<20 {
		return fmt.Errorf("inline answer exceeds retained result storage limit")
	}
	if err = fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return err
	}
	c.pruneInlinePages(filepath.Dir(path), path)
	return nil
}

func (c *TelegramChannel) loadInlinePages(inlineMessageID string) (telegramInlinePages, error) {
	path := c.inlinePagesPath(inlineMessageID)
	info, err := os.Stat(path)
	if err != nil {
		return telegramInlinePages{}, err
	}
	if time.Since(info.ModTime()) > inlinePagesLifetime {
		return telegramInlinePages{}, fmt.Errorf("inline answer expired")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return telegramInlinePages{}, err
	}
	var pages telegramInlinePages
	if err = json.Unmarshal(data, &pages); err != nil {
		return telegramInlinePages{}, err
	}
	return pages, nil
}

func (c *TelegramChannel) showInlinePage(ctx context.Context, inlineMessageID string, ownerID int64, page int) error {
	c.inlineMu.Lock()
	defer c.inlineMu.Unlock()
	pages, err := c.loadInlinePages(inlineMessageID)
	if err != nil {
		return err
	}
	if pages.OwnerID == 0 || pages.OwnerID != ownerID {
		return fmt.Errorf("inline answer belongs to another user")
	}
	if err = c.editInlinePage(ctx, inlineMessageID, pages, page); err != nil {
		return err
	}
	state := c.inlineReplyLocked(inlineMessageID)
	state.finalized = true
	state.retryText = ""
	return nil
}

// Bound persistent result state by age, count, and total bytes. Navigation of a
// retired result reports expiry; conversation history remains independent.
func (c *TelegramChannel) pruneInlinePages(directory, current string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	type storedPage struct {
		path string
		time time.Time
		size int64
	}
	files := []storedPage{}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if path != current && time.Since(info.ModTime()) > inlinePagesLifetime {
			_ = os.Remove(path)
			continue
		}
		files = append(files, storedPage{path, info.ModTime(), info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].time.Before(files[j].time) })
	for len(files) > 0 && (len(files) > 256 || total > 64<<20) {
		oldest := files[0]
		files = files[1:]
		if oldest.path == current {
			continue
		}
		if os.Remove(oldest.path) == nil {
			total -= oldest.size
		}
		if len(files) == 0 {
			break
		}
	}
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
		buttons = append(
			buttons,
			telego.InlineKeyboardButton{
				Text:         "← Previous",
				CallbackData: telegramInlinePagePrefix + strconv.Itoa(page-1),
			},
		)
	}
	if page+1 < len(chunks) {
		buttons = append(
			buttons,
			telego.InlineKeyboardButton{Text: "Next →", CallbackData: telegramInlinePagePrefix + strconv.Itoa(page+1)},
		)
	}
	return chunks[page] + fmt.Sprintf(
		"\n\nPage %d/%d",
		page+1,
		len(chunks),
	), &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{buttons},
	}, nil
}

func (c *TelegramChannel) editInlinePage(
	ctx context.Context,
	inlineMessageID string,
	pages telegramInlinePages,
	page int,
) error {
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
		richChunks := channels.SplitMessageWithOffsets(pages.Content, telegramRichMessageLimit-64)
		sourceOffset := richChunks[page].SourceOffset
		pages.Plain = true
		plainPage := 0
		for n, chunk := range channels.SplitMessageWithOffsets(pages.Content, telegramInlineMessageLimit-64) {
			if chunk.SourceOffset > sourceOffset {
				break
			}
			plainPage = n
		}
		if len([]rune(pages.Content)) > telegramInlineMessageLimit-64 {
			if err = c.saveInlinePages(inlineMessageID, pages); err != nil {
				return err
			}
		}
		return c.editInlinePage(ctx, inlineMessageID, pages, plainPage)
	}
	if isTelegramMessageNotModified(err) {
		err = nil
	}
	if err == nil {
		c.inlineReplyLocked(inlineMessageID).markup = params.ReplyMarkup
	}
	return telegramDeliveryError(err)
}

func (c *TelegramChannel) sendInlineAnswer(ctx context.Context, inlineMessageID, content string) error {
	c.inlineMu.Lock()
	defer c.inlineMu.Unlock()
	pages := telegramInlinePages{Content: normalizeRichMarkdown(content)}
	if strings.TrimSpace(pages.Content) == "" {
		return nil
	}
	_, ownerErr := c.loadInlinePages(inlineMessageID)
	if ownerErr == nil || len([]rune(pages.Content)) > telegramRichMessageLimit-64 {
		if err := c.saveInlinePages(inlineMessageID, pages); err != nil {
			return fmt.Errorf("save complete inline answer: %w", err)
		}
	}
	if err := c.editInlinePage(ctx, inlineMessageID, pages, 0); err != nil {
		return err
	}
	state := c.inlineReplyLocked(inlineMessageID)
	state.finalized = true
	state.retryText = ""
	return nil
}
