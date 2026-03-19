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

	sender := ToolSender(ctx)
	if strings.TrimSpace(sender.CanonicalID) == "" {
		return ErrorResult("personal todo requires sender identity")
	}

	channel := strings.TrimSpace(ToolChannel(ctx))
	chatID := strings.TrimSpace(ToolChatID(ctx))
	peerKind := strings.TrimSpace(ToolPeerKind(ctx))
	mode := detectPersonalTodoMode(channel, chatID, peerKind, sender)
	if mode == todoUnsupportedGroup {
		return ErrorResult("personal todo in this group is not supported. Open a direct chat with the bot and try again.")
	}

	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	scope := parseTodoScope(asString(args["scope"]))
	limit := max(1, asInt(args["limit"], 10))
	itemID := asInt(args["item_id"], 0)
	text := strings.TrimSpace(asString(args["text"]))

	switch action {
	case "add":
		if text == "" {
			return ErrorResult("text is required for add")
		}
		item, err := t.store.Add(ctx, sender.CanonicalID, text, channel, chatID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo add failed: %v", err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			renderPersonalTodoList([]personaltodo.Item{item}, personaltodo.Counts{Open: 1, Total: 1}, "added"),
			fmt.Sprintf("Added todo item #%d.", item.ItemID),
			fmt.Sprintf("Todo item #%d added to your list.", item.ItemID),
			false,
		)
	case "list":
		items, err := t.store.List(ctx, sender.CanonicalID, scope, limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo list failed: %v", err)).WithError(err)
		}
		counts, err := t.store.Counts(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo counts failed: %v", err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			renderPersonalTodoList(items, counts, todoScopeLabel(scope)),
			"Sent your todo list in a private message.",
			"",
			true,
		)
	case "status":
		counts, err := t.store.Counts(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo status failed: %v", err)).WithError(err)
		}
		items, err := t.store.List(ctx, sender.CanonicalID, personaltodo.StatusOpen, limit)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo status preview failed: %v", err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			renderPersonalTodoStatus(items, counts),
			"Sent your todo status in a private message.",
			"",
			true,
		)
	case "done":
		if itemID <= 0 {
			return ErrorResult("item_id is required for done")
		}
		item, err := t.store.MarkDone(ctx, sender.CanonicalID, itemID)
		if err != nil {
			return mapTodoStoreError("done", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf("Marked todo item #%d as done.", item.ItemID),
			fmt.Sprintf("Marked todo item #%d as done.", item.ItemID),
			fmt.Sprintf("Marked todo item #%d as done.", item.ItemID),
			false,
		)
	case "reopen":
		if itemID <= 0 {
			return ErrorResult("item_id is required for reopen")
		}
		item, err := t.store.Reopen(ctx, sender.CanonicalID, itemID)
		if err != nil {
			return mapTodoStoreError("reopen", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf("Reopened todo item #%d.", item.ItemID),
			fmt.Sprintf("Reopened todo item #%d.", item.ItemID),
			fmt.Sprintf("Reopened todo item #%d.", item.ItemID),
			false,
		)
	case "delete":
		if itemID <= 0 {
			return ErrorResult("item_id is required for delete")
		}
		if err := t.store.Delete(ctx, sender.CanonicalID, itemID); err != nil {
			return mapTodoStoreError("delete", err, itemID)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf("Deleted todo item #%d.", itemID),
			fmt.Sprintf("Deleted todo item #%d.", itemID),
			fmt.Sprintf("Deleted todo item #%d.", itemID),
			false,
		)
	case "clear_completed":
		count, err := t.store.ClearCompleted(ctx, sender.CanonicalID)
		if err != nil {
			return ErrorResult(fmt.Sprintf("personal todo clear completed failed: %v", err)).WithError(err)
		}
		return t.respond(ctx, mode, sender, channel, chatID,
			fmt.Sprintf("Cleared %d completed todo item(s).", count),
			"Cleared completed items from your todo list.",
			fmt.Sprintf("Cleared %d completed todo item(s).", count),
			false,
		)
	default:
		return ErrorResult("action must be one of add, list, status, done, reopen, delete, or clear_completed")
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
	switch mode {
	case todoDirect:
		if err := t.send(ctx, channel, chatID, prefixPersonalTodo(directPrefix, privateContent)); err != nil {
			return ErrorResult(fmt.Sprintf("personal todo delivery failed: %v", err)).WithError(err)
		}
		return SilentResult("Personal todo result sent to the user.")
	case todoTelegramGroup:
		if !privateInTelegramGroup {
			if err := t.send(ctx, channel, chatID, groupAck); err != nil {
				return ErrorResult(fmt.Sprintf("personal todo group acknowledgement failed: %v", err)).WithError(err)
			}
			return SilentResult("Personal todo acknowledgement sent to the group.")
		}
		if strings.TrimSpace(sender.PlatformID) == "" {
			return ErrorResult("personal todo requires a Telegram sender ID for private delivery")
		}
		if err := t.send(ctx, "telegram", sender.PlatformID, privateContent); err != nil {
			failClosed := "I could not send your todo privately. Open a direct chat with me in Telegram and retry."
			if ackErr := t.send(ctx, channel, chatID, failClosed); ackErr != nil {
				return ErrorResult(fmt.Sprintf("personal todo private delivery failed: %v", err)).WithError(err)
			}
			return SilentResult("Private Telegram delivery failed; asked the user to open a direct chat.")
		}
		if err := t.send(ctx, channel, chatID, groupAck); err != nil {
			return ErrorResult(fmt.Sprintf("personal todo group acknowledgement failed: %v", err)).WithError(err)
		}
		return SilentResult("Personal todo result sent privately with a public acknowledgement.")
	default:
		return ErrorResult("personal todo in this chat is not supported")
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

func mapTodoStoreError(action string, err error, itemID int) *ToolResult {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return ErrorResult(fmt.Sprintf("todo item #%d was not found", itemID))
	}
	return ErrorResult(fmt.Sprintf("personal todo %s failed: %v", action, err)).WithError(err)
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

func renderPersonalTodoList(items []personaltodo.Item, counts personaltodo.Counts, scope string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Personal todo (%s)\n", scope)
	fmt.Fprintf(&b, "Open: %d | Done: %d | Total: %d\n", counts.Open, counts.Done, counts.Total)
	if len(items) == 0 {
		b.WriteString("\nNo items in this view.")
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

func todoScopeLabel(scope personaltodo.Status) string {
	switch scope {
	case personaltodo.StatusDone:
		return "done"
	case personaltodo.StatusOpen:
		return "open"
	default:
		return "all"
	}
}

func renderPersonalTodoStatus(items []personaltodo.Item, counts personaltodo.Counts) string {
	var b strings.Builder
	b.WriteString("Personal todo status\n")
	fmt.Fprintf(&b, "Open: %d\nDone: %d\nTotal: %d", counts.Open, counts.Done, counts.Total)
	if len(items) > 0 {
		b.WriteString("\n\nOpen items preview:")
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
