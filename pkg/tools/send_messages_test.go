package tools

import (
	"context"
	"errors"
	"testing"
)

func TestSendMessagesTool_Execute_Success(t *testing.T) {
	tool := NewSendMessagesTool()

	type sentMsg struct {
		channel string
		chatID  string
		content string
	}
	var sent []sentMsg
	tool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		sent = append(sent, sentMsg{channel: channel, chatID: chatID, content: content})
		return nil
	})

	ctx := WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"messages": []any{
			map[string]any{"content": "Hallo"},
			map[string]any{"content": "Привет", "chat_id": "chat-2"},
		},
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !result.Silent || !result.Terminal {
		t.Fatalf("expected silent terminal result, got %#v", result)
	}
	if result.ForLLM != "Sent 2 messages" {
		t.Fatalf("unexpected ForLLM: %q", result.ForLLM)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 sent messages, got %d", len(sent))
	}
	if sent[0].channel != "telegram" || sent[0].chatID != "chat-1" || sent[0].content != "Hallo" {
		t.Fatalf("unexpected first send: %#v", sent[0])
	}
	if sent[1].channel != "telegram" || sent[1].chatID != "chat-2" || sent[1].content != "Привет" {
		t.Fatalf("unexpected second send: %#v", sent[1])
	}

	delivered := tool.DeliveredInRound()
	if len(delivered) != 2 {
		t.Fatalf("expected 2 delivered messages, got %d", len(delivered))
	}
}

func TestSendMessagesTool_Execute_PartialFailure(t *testing.T) {
	tool := NewSendMessagesTool()

	sendErr := errors.New("network error")
	callCount := 0
	tool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		callCount++
		if callCount == 2 {
			return sendErr
		}
		return nil
	})

	ctx := WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"messages": []any{
			map[string]any{"content": "one"},
			map[string]any{"content": "two"},
		},
	})

	if !result.IsError {
		t.Fatal("expected error")
	}
	if result.Err != sendErr {
		t.Fatalf("expected original error, got %v", result.Err)
	}
	if got := tool.DeliveredInRound(); len(got) != 1 {
		t.Fatalf("expected 1 delivered message before failure, got %#v", got)
	}
}

func TestSendMessagesTool_Execute_EmptyMessages(t *testing.T) {
	tool := NewSendMessagesTool()
	result := tool.Execute(context.Background(), map[string]any{
		"messages": []any{},
	})

	if !result.IsError {
		t.Fatal("expected error")
	}
	if result.ForLLM != "messages array must not be empty" {
		t.Fatalf("unexpected error: %q", result.ForLLM)
	}
}

func TestSendMessagesTool_DescriptionMentionsBatchUse(t *testing.T) {
	tool := NewSendMessagesTool()
	desc := tool.Description()
	if desc == "" {
		t.Fatal("description should not be empty")
	}
}
