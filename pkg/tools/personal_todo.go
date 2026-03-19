package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/personaltodo"
)

type PersonalTodoSendCallback func(ctx context.Context, channel, chatID, content string) error

type PersonalTodoTool struct {
	store        *personaltodo.Store
	sendCallback PersonalTodoSendCallback
	sentInRound  atomic.Bool
	mu           sync.Mutex
	delivered    []DeliveredMessage
}

func NewPersonalTodoTool(store *personaltodo.Store) *PersonalTodoTool {
	return &PersonalTodoTool{store: store}
}

func (t *PersonalTodoTool) Name() string {
	return "personal_todo"
}

func (t *PersonalTodoTool) Description() string {
	return "Manage a user's personal todo list. Use this for requests like 'add this to my tasks', 'what is in my todo', 'show my list', or 'mark item 3 done'. Do not store personal todo items in chat_memory or MEMORY.md."
}

func (t *PersonalTodoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "status", "done", "reopen", "delete", "clear_completed"},
				"description": "Todo action to perform",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Task text for add",
			},
			"item_id": map[string]any{
				"type":        "integer",
				"description": "Per-user todo item ID for done, reopen, or delete",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"open", "all", "done"},
				"description": "List scope. Default is open.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of todo items to show for list or status",
			},
		},
		"required": []string{"action"},
	}
}

func (t *PersonalTodoTool) ResetSentInRound() {
	t.sentInRound.Store(false)
	t.mu.Lock()
	t.delivered = nil
	t.mu.Unlock()
}

func (t *PersonalTodoTool) HasSentInRound() bool {
	return t.sentInRound.Load()
}

func (t *PersonalTodoTool) DeliveredInRound() []DeliveredMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]DeliveredMessage, len(t.delivered))
	copy(out, t.delivered)
	return out
}

func (t *PersonalTodoTool) SetSendCallback(callback PersonalTodoSendCallback) {
	t.sendCallback = callback
}

func (t *PersonalTodoTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.store == nil {
		return ErrorResult("personal todo is not configured")
	}
	tr := newPersonalTodoStrings(ToolLanguage(ctx))

	sender := ToolSender(ctx)
	if strings.TrimSpace(sender.CanonicalID) == "" {
		return ErrorResult(tr.senderRequired)
	}

	channel := strings.TrimSpace(ToolChannel(ctx))
	chatID := strings.TrimSpace(ToolChatID(ctx))
	peerKind := strings.TrimSpace(ToolPeerKind(ctx))
	mode := detectPersonalTodoMode(channel, chatID, peerKind, sender)
	if mode == todoUnsupportedGroup {
		return ErrorResult(tr.unsupportedGroup)
	}

	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	scope := parseTodoScope(asString(args["scope"]))
	limit := max(1, asInt(args["limit"], 10))
	itemID := asInt(args["item_id"], 0)
	text := strings.TrimSpace(asString(args["text"]))

	switch action {
	case "add":
		if text == "" {
			return ErrorResult(tr.textRequired)
		}
		item, err := t.store.Add(ctx, sender.CanonicalID, text, channel, chatID)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.addFailedFmt, err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			tr.renderList([]personaltodo.Item{item}, personaltodo.Counts{Open: 1, Total: 1}, tr.scopeAdded),
			fmt.Sprintf(tr.addedAckFmt, item.ItemID),
			fmt.Sprintf(tr.addedDirectFmt, item.ItemID),
			false,
		)
	case "list":
		items, err := t.store.List(ctx, sender.CanonicalID, scope, limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.listFailedFmt, err)).WithError(err)
		}
		counts, err := t.store.Counts(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.countsFailedFmt, err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			tr.renderList(items, counts, tr.scopeLabel(scope)),
			tr.sentListPrivate,
			"",
			true,
		)
	case "status":
		counts, err := t.store.Counts(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.statusFailedFmt, err)).WithError(err)
		}
		items, err := t.store.List(ctx, sender.CanonicalID, personaltodo.StatusOpen, limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.statusPreviewFailedFmt, err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			tr.renderStatus(items, counts),
			tr.sentStatusPrivate,
			"",
			true,
		)
	case "done":
		if itemID <= 0 {
			return ErrorResult(tr.itemIDRequiredDone)
		}
		item, err := t.store.MarkDone(ctx, sender.CanonicalID, itemID)
		if err != nil {
			return mapTodoStoreError(tr, "done", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf(tr.doneFmt, item.ItemID),
			fmt.Sprintf(tr.doneFmt, item.ItemID),
			fmt.Sprintf(tr.doneFmt, item.ItemID),
			false,
		)
	case "reopen":
		if itemID <= 0 {
			return ErrorResult(tr.itemIDRequiredReopen)
		}
		item, err := t.store.Reopen(ctx, sender.CanonicalID, itemID)
		if err != nil {
			return mapTodoStoreError(tr, "reopen", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf(tr.reopenFmt, item.ItemID),
			fmt.Sprintf(tr.reopenFmt, item.ItemID),
			fmt.Sprintf(tr.reopenFmt, item.ItemID),
			false,
		)
	case "delete":
		if itemID <= 0 {
			return ErrorResult(tr.itemIDRequiredDelete)
		}
		if err := t.store.Delete(ctx, sender.CanonicalID, itemID); err != nil {
			return mapTodoStoreError(tr, "delete", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf(tr.deleteFmt, itemID),
			fmt.Sprintf(tr.deleteFmt, itemID),
			fmt.Sprintf(tr.deleteFmt, itemID),
			false,
		)
	case "clear_completed":
		count, err := t.store.ClearCompleted(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf(tr.clearCompletedFailedFmt, err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf(tr.clearCompletedFmt, count),
			tr.clearedCompletedAck,
			fmt.Sprintf(tr.clearCompletedFmt, count),
			false,
		)
	default:
		return ErrorResult(tr.invalidAction)
	}
}

type todoMode int

const (
	todoDirect todoMode = iota
	todoTelegramGroup
	todoUnsupportedGroup
)

func detectPersonalTodoMode(channel, chatID, peerKind string, sender bus.SenderInfo) todoMode {
	if peerKind == "direct" {
		return todoDirect
	}
	if peerKind != "" && peerKind != "direct" {
		if channel == "telegram" {
			return todoTelegramGroup
		}
		return todoUnsupportedGroup
	}
	if channel == "telegram" {
		if sender.PlatformID != "" && chatID == sender.PlatformID {
			return todoDirect
		}
		if strings.HasPrefix(chatID, "-") || chatID != sender.PlatformID {
			return todoTelegramGroup
		}
	}
	if sender.PlatformID != "" && chatID == sender.PlatformID {
		return todoDirect
	}
	if chatID == "" || chatID == sender.PlatformID {
		return todoDirect
	}
	return todoUnsupportedGroup
}

func (t *PersonalTodoTool) respond(
	ctx context.Context,
	mode todoMode,
	sender bus.SenderInfo,
	channel, chatID, privateContent, groupAck, directPrefix string,
	privateInTelegramGroup bool,
) *ToolResult {
	tr := newPersonalTodoStrings(ToolLanguage(ctx))
	switch mode {
	case todoDirect:
		if err := t.send(ctx, channel, chatID, prefixPersonalTodo(directPrefix, privateContent)); err != nil {
			return ErrorResult(fmt.Sprintf(tr.deliveryFailedFmt, err)).WithError(err)
		}
		return SilentResult(tr.sentToUser)
	case todoTelegramGroup:
		if !privateInTelegramGroup {
			if err := t.send(ctx, channel, chatID, groupAck); err != nil {
				return ErrorResult(fmt.Sprintf(tr.groupAckFailedFmt, err)).WithError(err)
			}
			return SilentResult(tr.groupAckSent)
		}
		if strings.TrimSpace(sender.PlatformID) == "" {
			return ErrorResult(tr.telegramSenderRequired)
		}
		if err := t.send(ctx, "telegram", sender.PlatformID, privateContent); err != nil {
			failClosed := tr.failClosedOpenDM
			if ackErr := t.send(ctx, channel, chatID, failClosed); ackErr != nil {
				return ErrorResult(fmt.Sprintf(tr.privateDeliveryFailedFmt, err)).WithError(err)
			}
			return SilentResult(tr.privateDeliveryFailedHandled)
		}
		if err := t.send(ctx, channel, chatID, groupAck); err != nil {
			return ErrorResult(fmt.Sprintf(tr.groupAckFailedFmt, err)).WithError(err)
		}
		return SilentResult(tr.privatePlusAckSent)
	default:
		return ErrorResult(tr.unsupportedChat)
	}
}

func (t *PersonalTodoTool) send(ctx context.Context, channel, chatID, content string) error {
	if t.sendCallback == nil {
		return fmt.Errorf("send callback is not configured")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if err := t.sendCallback(ctx, channel, chatID, content); err != nil {
		return err
	}
	t.mu.Lock()
	t.delivered = append(t.delivered, DeliveredMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	})
	t.mu.Unlock()
	t.sentInRound.Store(true)
	return nil
}

func mapTodoStoreError(tr personalTodoStrings, action string, err error, itemID int) *ToolResult {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return ErrorResult(fmt.Sprintf(tr.notFoundFmt, itemID))
	}
	return ErrorResult(fmt.Sprintf(tr.actionFailedFmt(action), err)).WithError(err)
}

func parseTodoScope(raw string) personaltodo.Status {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done":
		return personaltodo.StatusDone
	case "all":
		return ""
	default:
		return personaltodo.StatusOpen
	}
}

func renderPersonalTodoList(items []personaltodo.Item, counts personaltodo.Counts, scope, title, countsFmt, noItems string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", title, scope)
	fmt.Fprintf(&b, countsFmt+"\n", counts.Open, counts.Done, counts.Total)
	if len(items) == 0 {
		b.WriteString("\n" + noItems)
		return b.String()
	}
	for _, item := range items {
		marker := "[ ]"
		if item.Status == personaltodo.StatusDone {
			marker = "[x]"
		}
		fmt.Fprintf(&b, "\n%s #%d %s", marker, item.ItemID, item.Text)
	}
	return b.String()
}

func renderPersonalTodoStatus(items []personaltodo.Item, counts personaltodo.Counts, title, countsFmt, previewTitle string) string {
	var b strings.Builder
	b.WriteString(title + "\n")
	fmt.Fprintf(&b, countsFmt, counts.Open, counts.Done, counts.Total)
	if len(items) > 0 {
		b.WriteString("\n\n" + previewTitle + ":")
		for _, item := range items {
			fmt.Fprintf(&b, "\n- #%d %s", item.ItemID, item.Text)
		}
	}
	return b.String()
}

func prefixPersonalTodo(prefix, body string) string {
	body = strings.TrimSpace(body)
	if prefix == "" {
		return body
	}
	if body == "" {
		return prefix
	}
	return prefix + "\n\n" + body
}

type personalTodoStrings struct {
	senderRequired               string
	unsupportedGroup             string
	textRequired                 string
	addFailedFmt                 string
	listFailedFmt                string
	countsFailedFmt              string
	statusFailedFmt              string
	statusPreviewFailedFmt       string
	itemIDRequiredDone           string
	itemIDRequiredReopen         string
	itemIDRequiredDelete         string
	clearCompletedFailedFmt      string
	invalidAction                string
	deliveryFailedFmt            string
	groupAckFailedFmt            string
	telegramSenderRequired       string
	privateDeliveryFailedFmt     string
	failClosedOpenDM             string
	privateDeliveryFailedHandled string
	privatePlusAckSent           string
	groupAckSent                 string
	sentToUser                   string
	unsupportedChat              string
	notFoundFmt                  string
	doneFailedFmt                string
	reopenFailedFmt              string
	deleteFailedFmt              string
	addedAckFmt                  string
	addedDirectFmt               string
	doneFmt                      string
	reopenFmt                    string
	deleteFmt                    string
	clearCompletedFmt            string
	clearedCompletedAck          string
	sentListPrivate              string
	sentStatusPrivate            string
	scopeAdded                   string
	titleList                    string
	titleStatus                  string
	countsLine                   string
	countsBlock                  string
	noItems                      string
	openPreview                  string
	scopeOpen                    string
	scopeDone                    string
	scopeAll                     string
}

func newPersonalTodoStrings(lang string) personalTodoStrings {
	if strings.EqualFold(strings.TrimSpace(lang), "ru") {
		return personalTodoStrings{
			senderRequired:               "personal todo требует идентичность отправителя",
			unsupportedGroup:             "В этой группе personal todo не поддерживается. Открой личку с ботом и повтори запрос.",
			textRequired:                 "для add нужен text",
			addFailedFmt:                 "не удалось добавить personal todo: %v",
			listFailedFmt:                "не удалось показать personal todo: %v",
			countsFailedFmt:              "не удалось получить счётчики personal todo: %v",
			statusFailedFmt:              "не удалось получить статус personal todo: %v",
			statusPreviewFailedFmt:       "не удалось получить preview personal todo: %v",
			itemIDRequiredDone:           "для done нужен item_id",
			itemIDRequiredReopen:         "для reopen нужен item_id",
			itemIDRequiredDelete:         "для delete нужен item_id",
			clearCompletedFailedFmt:      "не удалось очистить выполненные personal todo: %v",
			invalidAction:                "action должен быть одним из add, list, status, done, reopen, delete или clear_completed",
			deliveryFailedFmt:            "не удалось отправить personal todo: %v",
			groupAckFailedFmt:            "не удалось отправить подтверждение в группу: %v",
			telegramSenderRequired:       "для приватной доставки personal todo нужен Telegram sender ID",
			privateDeliveryFailedFmt:     "не удалось доставить personal todo в личку: %v",
			failClosedOpenDM:             "Не смог отправить твой список в личку. Открой direct chat со мной в Telegram и повтори запрос.",
			privateDeliveryFailedHandled: "Приватная доставка в Telegram не удалась; попросил пользователя открыть личку.",
			privatePlusAckSent:           "Результат personal todo отправлен в личку, а в группу ушло только подтверждение.",
			groupAckSent:                 "Подтверждение personal todo отправлено в группу.",
			sentToUser:                   "Результат personal todo отправлен пользователю.",
			unsupportedChat:              "В этом чате personal todo не поддерживается",
			notFoundFmt:                  "пункт todo #%d не найден",
			doneFailedFmt:                "не удалось отметить задачу выполненной: %v",
			reopenFailedFmt:              "не удалось переоткрыть задачу: %v",
			deleteFailedFmt:              "не удалось удалить задачу: %v",
			addedAckFmt:                  "Добавил задачу #%d.",
			addedDirectFmt:               "Задача #%d добавлена в твой список.",
			doneFmt:                      "Отметил задачу #%d выполненной.",
			reopenFmt:                    "Снова открыл задачу #%d.",
			deleteFmt:                    "Удалил задачу #%d.",
			clearCompletedFmt:            "Очистил %d выполненных задач.",
			clearedCompletedAck:          "Очистил выполненные задачи из твоего списка.",
			sentListPrivate:              "Отправил твой список дел в личку.",
			sentStatusPrivate:            "Отправил статус твоих задач в личку.",
			scopeAdded:                   "добавлено",
			titleList:                    "Личный список дел",
			titleStatus:                  "Статус личных задач",
			countsLine:                   "Открыто: %d | Выполнено: %d | Всего: %d",
			countsBlock:                  "Открыто: %d\nВыполнено: %d\nВсего: %d",
			noItems:                      "В этом представлении задач нет.",
			openPreview:                  "Превью открытых задач",
			scopeOpen:                    "открытые",
			scopeDone:                    "выполненные",
			scopeAll:                     "все",
		}
	}

	return personalTodoStrings{
		senderRequired:               "personal todo requires sender identity",
		unsupportedGroup:             "personal todo in this group is not supported. Open a direct chat with the bot and try again.",
		textRequired:                 "text is required for add",
		addFailedFmt:                 "personal todo add failed: %v",
		listFailedFmt:                "personal todo list failed: %v",
		countsFailedFmt:              "personal todo counts failed: %v",
		statusFailedFmt:              "personal todo status failed: %v",
		statusPreviewFailedFmt:       "personal todo status preview failed: %v",
		itemIDRequiredDone:           "item_id is required for done",
		itemIDRequiredReopen:         "item_id is required for reopen",
		itemIDRequiredDelete:         "item_id is required for delete",
		clearCompletedFailedFmt:      "personal todo clear completed failed: %v",
		invalidAction:                "action must be one of add, list, status, done, reopen, delete, or clear_completed",
		deliveryFailedFmt:            "personal todo delivery failed: %v",
		groupAckFailedFmt:            "personal todo group acknowledgement failed: %v",
		telegramSenderRequired:       "personal todo requires a Telegram sender ID for private delivery",
		privateDeliveryFailedFmt:     "personal todo private delivery failed: %v",
		failClosedOpenDM:             "I could not send your todo privately. Open a direct chat with me in Telegram and retry.",
		privateDeliveryFailedHandled: "Private Telegram delivery failed; asked the user to open a direct chat.",
		privatePlusAckSent:           "Personal todo result sent privately with a public acknowledgement.",
		groupAckSent:                 "Personal todo acknowledgement sent to the group.",
		sentToUser:                   "Personal todo result sent to the user.",
		unsupportedChat:              "personal todo in this chat is not supported",
		notFoundFmt:                  "todo item #%d was not found",
		doneFailedFmt:                "personal todo done failed: %v",
		reopenFailedFmt:              "personal todo reopen failed: %v",
		deleteFailedFmt:              "personal todo delete failed: %v",
		addedAckFmt:                  "Added todo item #%d.",
		addedDirectFmt:               "Todo item #%d added to your list.",
		doneFmt:                      "Marked todo item #%d as done.",
		reopenFmt:                    "Reopened todo item #%d.",
		deleteFmt:                    "Deleted todo item #%d.",
		clearCompletedFmt:            "Cleared %d completed todo item(s).",
		clearedCompletedAck:          "Cleared completed items from your todo list.",
		sentListPrivate:              "Sent your todo list in a private message.",
		sentStatusPrivate:            "Sent your todo status in a private message.",
		scopeAdded:                   "added",
		titleList:                    "Personal todo",
		titleStatus:                  "Personal todo status",
		countsLine:                   "Open: %d | Done: %d | Total: %d",
		countsBlock:                  "Open: %d\nDone: %d\nTotal: %d",
		noItems:                      "No items in this view.",
		openPreview:                  "Open items preview",
		scopeOpen:                    "open",
		scopeDone:                    "done",
		scopeAll:                     "all",
	}
}

func (tr personalTodoStrings) scopeLabel(scope personaltodo.Status) string {
	switch scope {
	case personaltodo.StatusDone:
		return tr.scopeDone
	case personaltodo.StatusOpen:
		return tr.scopeOpen
	default:
		return tr.scopeAll
	}
}

func (tr personalTodoStrings) renderList(items []personaltodo.Item, counts personaltodo.Counts, scope string) string {
	return renderPersonalTodoList(items, counts, scope, tr.titleList, tr.countsLine, tr.noItems)
}

func (tr personalTodoStrings) renderStatus(items []personaltodo.Item, counts personaltodo.Counts) string {
	return renderPersonalTodoStatus(items, counts, tr.titleStatus, tr.countsBlock, tr.openPreview)
}

func (tr personalTodoStrings) actionFailedFmt(action string) string {
	switch action {
	case "done":
		return tr.doneFailedFmt
	case "reopen":
		return tr.reopenFailedFmt
	case "delete":
		return tr.deleteFailedFmt
	}
	return fmt.Sprintf("personal todo %s failed: %%v", action)
}
