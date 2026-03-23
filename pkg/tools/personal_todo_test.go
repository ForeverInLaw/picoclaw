package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/personaltodo"
)

type sentMessage struct {
	channel string
	chatID  string
	content string
}

func newPersonalTodoToolForTest(t *testing.T) (*PersonalTodoTool, *[]sentMessage) {
	t.Helper()
	store, err := personaltodo.Open(filepath.Join(t.TempDir(), "personal_todos.sqlite"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var sent []sentMessage
	tool := NewPersonalTodoTool(store)
	tool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		sent = append(sent, sentMessage{channel: channel, chatID: chatID, content: content})
		return nil
	})
	return tool, &sent
}

func TestPersonalTodoTool_Description_ExplainsUsage(t *testing.T) {
	tool := NewPersonalTodoTool(nil)
	desc := tool.Description()
	for _, needle := range []string{
		"personal todo list",
		"add this to my tasks",
		"chat_memory or MEMORY.md",
	} {
		if !strings.Contains(desc, needle) {
			t.Fatalf("Description() missing %q: %s", needle, desc)
		}
	}
}

func TestPersonalTodoTool_Execute_DMFlow(t *testing.T) {
	tool, sent := newPersonalTodoToolForTest(t)
	ctx := WithToolLanguage(WithToolSender(WithToolContext(t.Context(), "telegram", "42"), bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		DisplayName: "Ser",
	}), "en")

	addResult := tool.Execute(ctx, map[string]any{
		"action": "add",
		"text":   "buy milk",
	})
	if addResult.IsError {
		t.Fatalf("add Execute() error: %s", addResult.ForLLM)
	}
	if !addResult.Terminal {
		t.Fatal("expected add result to be terminal")
	}
	if len(*sent) != 1 || (*sent)[0].chatID != "42" || !strings.Contains((*sent)[0].content, "buy milk") {
		t.Fatalf("unexpected sent messages after add: %#v", *sent)
	}

	listResult := tool.Execute(ctx, map[string]any{
		"action": "list",
		"scope":  "open",
	})
	if listResult.IsError {
		t.Fatalf("list Execute() error: %s", listResult.ForLLM)
	}
	if !listResult.Terminal {
		t.Fatal("expected list result to be terminal")
	}
	if len(*sent) != 2 || !strings.Contains((*sent)[1].content, "#1 buy milk") {
		t.Fatalf("unexpected sent messages after list: %#v", *sent)
	}
}

func TestPersonalTodoTool_Execute_TelegramGroupPrivacy(t *testing.T) {
	tool, sent := newPersonalTodoToolForTest(t)
	ctx := WithToolLanguage(WithToolSender(WithToolContext(t.Context(), "telegram", "-1001"), bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		DisplayName: "Ser",
	}), "en")

	addResult := tool.Execute(ctx, map[string]any{
		"action": "add",
		"text":   "secret migration plan",
	})
	if addResult.IsError {
		t.Fatalf("add Execute() error: %s", addResult.ForLLM)
	}
	if !addResult.Terminal {
		t.Fatal("expected add result to be terminal")
	}
	if len(*sent) != 1 {
		t.Fatalf("group add should send one public ack, got %#v", *sent)
	}
	if (*sent)[0].chatID != "-1001" || strings.Contains((*sent)[0].content, "secret migration plan") {
		t.Fatalf("group add leaked private text: %#v", (*sent)[0])
	}

	listResult := tool.Execute(ctx, map[string]any{"action": "list"})
	if listResult.IsError {
		t.Fatalf("list Execute() error: %s", listResult.ForLLM)
	}
	if !listResult.Terminal {
		t.Fatal("expected list result to be terminal")
	}
	if len(*sent) != 3 {
		t.Fatalf("group list should send DM + ack, got %#v", *sent)
	}
	if (*sent)[1].chatID != "42" || !strings.Contains((*sent)[1].content, "secret migration plan") {
		t.Fatalf("expected private DM with todo text, got %#v", (*sent)[1])
	}
	if (*sent)[2].chatID != "-1001" || strings.Contains((*sent)[2].content, "secret migration plan") {
		t.Fatalf("expected public ack without leak, got %#v", (*sent)[2])
	}
}

func TestPersonalTodoTool_Execute_NonTelegramGroupBlocked(t *testing.T) {
	tool, sent := newPersonalTodoToolForTest(t)
	ctx := WithToolLanguage(WithToolSender(WithToolContext(t.Context(), "discord", "group-1"), bus.SenderInfo{
		Platform:    "discord",
		PlatformID:  "42",
		CanonicalID: "discord:42",
		DisplayName: "Ser",
	}), "en")

	result := tool.Execute(ctx, map[string]any{
		"action": "add",
		"text":   "should not work",
	})
	if !result.IsError {
		t.Fatalf("expected error, got %#v", result)
	}
	if len(*sent) != 0 {
		t.Fatalf("expected no messages sent, got %#v", *sent)
	}
	if !strings.Contains(result.ForLLM, "not supported") {
		t.Fatalf("unexpected error message: %q", result.ForLLM)
	}
}

func TestPersonalTodoTool_Execute_TelegramGroupList_FailsClosedWhenDMUnavailable(t *testing.T) {
	store, err := personaltodo.Open(filepath.Join(t.TempDir(), "personal_todos.sqlite"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	if _, err := store.Add(t.Context(), "telegram:42", "top secret", "telegram", "-1001"); err != nil {
		t.Fatalf("Add() error: %v", err)
	}

	var sent []sentMessage
	tool := NewPersonalTodoTool(store)
	tool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		if channel == "telegram" && chatID == "42" {
			return context.DeadlineExceeded
		}
		sent = append(sent, sentMessage{channel: channel, chatID: chatID, content: content})
		return nil
	})

	ctx := WithToolLanguage(WithToolSender(WithToolContext(t.Context(), "telegram", "-1001"), bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		DisplayName: "Ser",
	}), "en")
	result := tool.Execute(ctx, map[string]any{"action": "list"})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}
	if len(sent) != 1 {
		t.Fatalf("expected only one safe group ack, got %#v", sent)
	}
	if sent[0].chatID != "-1001" || strings.Contains(sent[0].content, "top secret") {
		t.Fatalf("unexpected fail-closed ack: %#v", sent[0])
	}
	if !strings.Contains(sent[0].content, "Open a direct chat") {
		t.Fatalf("expected DM guidance in ack, got %q", sent[0].content)
	}
}

func TestPersonalTodoTool_Execute_RussianLanguageHint(t *testing.T) {
	tool, sent := newPersonalTodoToolForTest(t)
	ctx := WithToolLanguage(WithToolSender(WithToolContext(t.Context(), "telegram", "42"), bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		DisplayName: "Ser",
	}), "ru")

	addResult := tool.Execute(ctx, map[string]any{
		"action": "add",
		"text":   "Передать саламалекум Андрею",
	})
	if addResult.IsError {
		t.Fatalf("add Execute() error: %s", addResult.ForLLM)
	}

	listResult := tool.Execute(ctx, map[string]any{"action": "list"})
	if listResult.IsError {
		t.Fatalf("list Execute() error: %s", listResult.ForLLM)
	}
	if len(*sent) != 2 {
		t.Fatalf("expected 2 sent messages, got %#v", *sent)
	}
	if !strings.Contains((*sent)[1].content, "Личный список дел") {
		t.Fatalf("expected Russian title, got %q", (*sent)[1].content)
	}
	if !strings.Contains((*sent)[1].content, "Открыто: 1 | Выполнено: 0 | Всего: 1") {
		t.Fatalf("expected Russian counters, got %q", (*sent)[1].content)
	}
}
