package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/sherlock"
)

const (
	sherlockCallbackPrefix = "__sherlock__"
	sherlockPageSize       = 8
	sherlockMessageLimit   = 3900
)

func (c *TelegramChannel) tryHandleSherlockCommand(ctx context.Context, message *telego.Message) (bool, error) {
	if c == nil || c.sherlock == nil || message == nil || message.From == nil {
		return false, nil
	}

	messageText, _ := telegramEntityTextAndList(message)
	cmdName, ok := commands.CommandName(strings.TrimSpace(messageText))
	if !ok || cmdName != "sherlock" {
		return false, nil
	}
	if message.Chat.Type != "private" {
		return true, c.replySherlockUnavailable(ctx, message.Chat.ID, "Sherlock доступен только в личке.")
	}

	return true, c.sendSherlockUserList(ctx, message.Chat.ID, fmt.Sprintf("%d", message.From.ID), 0)
}

func (c *TelegramChannel) observeSherlockMessage(ctx context.Context, message *telego.Message) {
	if c == nil || c.sherlock == nil || message == nil || message.From == nil {
		return
	}
	if message.Chat.Type != "private" || message.From.IsBot {
		return
	}

	sender := c.telegramSenderInfo(message.From)
	if !c.IsAllowedSender(sender) {
		return
	}

	text := normalizeSherlockText(message.Text, message.Caption)
	if text == "" || commands.HasCommandPrefix(text) {
		return
	}

	if err := c.sherlock.RecordMessage(ctx, sender.PlatformID, sender.Username, sender.DisplayName, text); err != nil {
		logger.WarnCF("telegram", "Sherlock record failed", map[string]any{"error": err.Error()})
	}
}

func normalizeSherlockText(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (c *TelegramChannel) handleSherlockCallbackQuery(ctx *th.Context, query telego.CallbackQuery) error {
	kind, requesterID, targetUserID, page, ok := parseSherlockCallbackData(query.Data)
	if !ok {
		return nil
	}
	if query.From.ID != 0 && requesterID != "" && requesterID != fmt.Sprintf("%d", query.From.ID) {
		return ctx.Bot().AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Эта кнопка не для тебя.",
			ShowAlert:       false,
		})
	}

	if query.Message == nil {
		return ctx.Bot().AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID))
	}

	var err error
	switch kind {
	case "list":
		err = c.editSherlockUserList(ctx, query.Message.GetChat().ID, query.Message.GetMessageID(), requesterID, page)
	case "user":
		err = c.editSherlockUserMessages(ctx, query.Message.GetChat().ID, query.Message.GetMessageID(), requesterID, targetUserID, page)
	default:
		return nil
	}
	if err != nil {
		logger.WarnCF("telegram", "Sherlock callback failed", map[string]any{"error": err.Error()})
		return ctx.Bot().AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Sherlock failed",
			ShowAlert:       false,
		})
	}
	return ctx.Bot().AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID))
}

func (c *TelegramChannel) sendSherlockUserList(ctx context.Context, chatID int64, requesterID string, page int) error {
	text, markup, err := c.buildSherlockUserList(ctx, requesterID, page)
	if err != nil {
		return err
	}
	_, err = c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      tu.ID(chatID),
		Text:        text,
		ReplyMarkup: markup,
	})
	return err
}

func (c *TelegramChannel) editSherlockUserList(ctx context.Context, chatID int64, messageID int, requesterID string, page int) error {
	text, markup, err := c.buildSherlockUserList(ctx, requesterID, page)
	if err != nil {
		return err
	}
	_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      tu.ID(chatID),
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if isTelegramMessageNotModified(err) {
		return nil
	}
	return err
}

func (c *TelegramChannel) editSherlockUserMessages(ctx context.Context, chatID int64, messageID int, requesterID, userID string, page int) error {
	text, markup, err := c.buildSherlockUserMessages(ctx, requesterID, userID, page)
	if err != nil {
		return err
	}
	_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      tu.ID(chatID),
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if isTelegramMessageNotModified(err) {
		return nil
	}
	return err
}

func (c *TelegramChannel) buildSherlockUserList(ctx context.Context, requesterID string, page int) (string, *telego.InlineKeyboardMarkup, error) {
	if page < 0 {
		page = 0
	}
	offset := page * sherlockPageSize
	users, total, err := c.sherlock.ListUsers(ctx, offset, sherlockPageSize)
	if err != nil {
		return "", nil, err
	}
	if total == 0 {
		return "Sherlock пуст. Нет сохранённых личных сообщений.", nil, nil
	}
	if offset >= total && page > 0 {
		page = maxSherlockPage(total)
		offset = page * sherlockPageSize
		users, total, err = c.sherlock.ListUsers(ctx, offset, sherlockPageSize)
		if err != nil {
			return "", nil, err
		}
	}

	start := offset + 1
	end := offset + len(users)
	text := fmt.Sprintf("Sherlock\n\nПользователи %d-%d из %d:", start, end, total)
	rows := make([][]telego.InlineKeyboardButton, 0, len(users)+1)
	for _, user := range users {
		rows = append(rows, []telego.InlineKeyboardButton{{
			Text:         formatSherlockUserButton(user),
			CallbackData: sherlockUserCallbackData(requesterID, user.UserID, page),
		}})
	}
	if nav := sherlockNavRow(requesterID, page, total); len(nav) > 0 {
		rows = append(rows, nav)
	}
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (c *TelegramChannel) buildSherlockUserMessages(ctx context.Context, requesterID, userID string, page int) (string, *telego.InlineKeyboardMarkup, error) {
	user, ok, err := c.sherlock.GetUser(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		text, markup, listErr := c.buildSherlockUserList(ctx, requesterID, page)
		if listErr != nil {
			return "", nil, listErr
		}
		if markup == nil {
			return "Пользователь уже исчез из Sherlock.", nil, nil
		}
		return "Пользователь уже исчез из Sherlock.\n\n" + text, markup, nil
	}

	messages, err := c.sherlock.GetMessages(ctx, userID, 20)
	if err != nil {
		return "", nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Sherlock: %s\nID: %s\nВсего сохранено: %d\n\n", formatSherlockUserLabel(user), user.UserID, user.MessageCount)
	if len(messages) == 0 {
		b.WriteString("Нет сохранённых сообщений.")
	} else {
		b.WriteString("Последние 20 текстовых сообщений:\n")
		for i, message := range messages {
			line := fmt.Sprintf("\n%d. [%s] %s", i+1, message.CreatedAt.In(time.Local).Format("2006-01-02 15:04"), compactSherlockMessage(message.Text))
			if b.Len()+len(line) > sherlockMessageLimit {
				b.WriteString("\n\n...[обрезано по длине]")
				break
			}
			b.WriteString(line)
		}
	}

	markup := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{{
		Text:         "← Назад",
		CallbackData: sherlockListCallbackData(requesterID, page),
	}}}}
	return b.String(), markup, nil
}

func (c *TelegramChannel) replySherlockUnavailable(ctx context.Context, chatID int64, text string) error {
	_, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{ChatID: tu.ID(chatID), Text: text})
	return err
}

func parseSherlockCallbackData(raw string) (kind, requesterID, userID string, page int, ok bool) {
	parts := strings.Split(raw, ":")
	if len(parts) < 4 || parts[0] != sherlockCallbackPrefix {
		return "", "", "", 0, false
	}
	kind = parts[1]
	requesterID = parts[2]
	switch kind {
	case "list":
		if len(parts) != 4 {
			return "", "", "", 0, false
		}
		parsedPage, err := strconv.Atoi(parts[3])
		if err != nil || parsedPage < 0 {
			return "", "", "", 0, false
		}
		return kind, requesterID, "", parsedPage, true
	case "user":
		if len(parts) != 5 {
			return "", "", "", 0, false
		}
		parsedPage, err := strconv.Atoi(parts[4])
		if err != nil || parsedPage < 0 {
			return "", "", "", 0, false
		}
		return kind, requesterID, parts[3], parsedPage, true
	default:
		return "", "", "", 0, false
	}
}

func sherlockListCallbackData(requesterID string, page int) string {
	return fmt.Sprintf("%s:list:%s:%d", sherlockCallbackPrefix, requesterID, page)
}

func sherlockUserCallbackData(requesterID, userID string, page int) string {
	return fmt.Sprintf("%s:user:%s:%s:%d", sherlockCallbackPrefix, requesterID, userID, page)
}

func sherlockNavRow(requesterID string, page, total int) []telego.InlineKeyboardButton {
	lastPage := maxSherlockPage(total)
	row := make([]telego.InlineKeyboardButton, 0, 2)
	if page > 0 {
		row = append(row, telego.InlineKeyboardButton{Text: "←", CallbackData: sherlockListCallbackData(requesterID, page-1)})
	}
	if page < lastPage {
		row = append(row, telego.InlineKeyboardButton{Text: "→", CallbackData: sherlockListCallbackData(requesterID, page+1)})
	}
	return row
}

func maxSherlockPage(total int) int {
	if total <= 0 {
		return 0
	}
	return (total - 1) / sherlockPageSize
}

func formatSherlockUserButton(user sherlock.User) string {
	label := formatSherlockUserLabel(user)
	if user.MessageCount > 0 {
		return fmt.Sprintf("%s · %d", label, user.MessageCount)
	}
	return label
}

func formatSherlockUserLabel(user sherlock.User) string {
	label := strings.TrimSpace(user.DisplayLabel)
	if label == "" {
		label = strings.TrimSpace(strings.TrimPrefix(user.Username, "@"))
	}
	if label == "" {
		label = user.UserID
	}
	if user.UserID != "" && label != user.UserID {
		return fmt.Sprintf("%s (%s)", label, user.UserID)
	}
	return label
}

func compactSherlockMessage(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= 140 {
		return string(runes)
	}
	return string(runes[:140]) + "…"
}
