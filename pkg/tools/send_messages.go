package tools

import (
	"context"
	"fmt"
)

type SendMessagesTool struct {
	sendCallback SendCallback
	tracker      directDeliveryTracker
}

func NewSendMessagesTool() *SendMessagesTool {
	return &SendMessagesTool{}
}

func (t *SendMessagesTool) Name() string {
	return "send_messages"
}

func (t *SendMessagesTool) Description() string {
	return "Send multiple outbound messages in one tool call. Use this when you need to send more than one message, more than one language variant, or different destinations/chats in the same step. Do not pack multiple JSON payloads into message; use send_messages instead."
}

func (t *SendMessagesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"messages": map[string]any{
				"type":        "array",
				"description": "List of outbound messages. Each item sends exactly one message.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "The message content to send",
						},
						"channel": map[string]any{
							"type":        "string",
							"description": "Optional: target channel (telegram, whatsapp, etc.). Defaults to the current channel.",
						},
						"chat_id": map[string]any{
							"type":        "string",
							"description": "Optional: target chat/user ID. Defaults to the current chat.",
						},
					},
					"required":             []string{"content"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"messages"},
		"additionalProperties": false,
	}
}

func (t *SendMessagesTool) ResetSentInRound() {
	t.tracker.ResetSentInRound()
}

func (t *SendMessagesTool) HasSentInRound() bool {
	return t.tracker.HasSentInRound()
}

func (t *SendMessagesTool) DeliveredInRound() []DeliveredMessage {
	return t.tracker.DeliveredInRound()
}

func (t *SendMessagesTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *SendMessagesTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	items, ok := args["messages"].([]any)
	if !ok {
		return &ToolResult{ForLLM: "messages array is required", IsError: true}
	}
	if len(items) == 0 {
		return &ToolResult{ForLLM: "messages array must not be empty", IsError: true}
	}
	if t.sendCallback == nil {
		return &ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	sent := 0
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return &ToolResult{ForLLM: fmt.Sprintf("message[%d] is invalid", i), IsError: true}
		}
		content, ok := item["content"].(string)
		if !ok {
			return &ToolResult{ForLLM: fmt.Sprintf("message[%d].content is required", i), IsError: true}
		}

		channel, _ := item["channel"].(string)
		chatID, _ := item["chat_id"].(string)
		if channel == "" {
			channel = ToolChannel(ctx)
		}
		if chatID == "" {
			chatID = ToolChatID(ctx)
		}
		if channel == "" || chatID == "" {
			return &ToolResult{
				ForLLM:  fmt.Sprintf("message[%d]: No target channel/chat specified", i),
				IsError: true,
			}
		}

		if err := t.sendCallback(ctx, channel, chatID, content); err != nil {
			return (&ToolResult{
				ForLLM:  fmt.Sprintf("sent %d/%d messages; sending message[%d]: %v", sent, len(items), i, err),
				IsError: true,
				Err:     err,
			})
		}

		t.tracker.recordDelivered(channel, chatID, content)
		sent++
	}

	return (&ToolResult{
		ForLLM: fmt.Sprintf("Sent %d messages", sent),
		Silent: true,
	}).WithTerminal()
}
