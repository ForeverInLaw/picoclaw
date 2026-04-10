package tools

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

type SendCallback func(ctx context.Context, channel, chatID, content string) error

type directDeliveryTracker struct {
	sentInRound atomic.Bool
	mu          sync.Mutex
	delivered   []DeliveredMessage
}

func (t *directDeliveryTracker) ResetSentInRound() {
	t.sentInRound.Store(false)
	t.mu.Lock()
	t.delivered = nil
	t.mu.Unlock()
}

func (t *directDeliveryTracker) HasSentInRound() bool {
	return t.sentInRound.Load()
}

func (t *directDeliveryTracker) DeliveredInRound() []DeliveredMessage {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]DeliveredMessage, len(t.delivered))
	copy(out, t.delivered)
	return out
}

func (t *directDeliveryTracker) recordDelivered(channel, chatID, content string) {
	t.mu.Lock()
	t.delivered = append(t.delivered, DeliveredMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	})
	t.mu.Unlock()
	t.sentInRound.Store(true)
}

type MessageTool struct {
	sendCallback SendCallback
	tracker      directDeliveryTracker
}

func NewMessageTool() *MessageTool {
	return &MessageTool{}
}

func (t *MessageTool) Name() string {
	return "message"
}

func (t *MessageTool) Description() string {
	return "Send exactly one outbound message to one chat/channel. Use this for a single reply or notification. If you need to send multiple messages, languages, or destinations in one tool call, use send_messages instead."
}

func (t *MessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The message content to send",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Optional: target channel (telegram, whatsapp, etc.)",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Optional: target chat/user ID",
			},
		},
		"required":             []string{"content"},
		"additionalProperties": false,
	}
}

// ResetSentInRound resets the per-round send tracker.
// Called by the agent loop at the start of each inbound message processing round.
func (t *MessageTool) ResetSentInRound() {
	t.tracker.ResetSentInRound()
}

// HasSentInRound returns true if the message tool sent a message during the current round.
func (t *MessageTool) HasSentInRound() bool {
	return t.tracker.HasSentInRound()
}

func (t *MessageTool) DeliveredInRound() []DeliveredMessage {
	return t.tracker.DeliveredInRound()
}

func (t *MessageTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	content, ok := args["content"].(string)
	if !ok {
		return &ToolResult{ForLLM: "content is required", IsError: true}
	}

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)

	if channel == "" {
		channel = ToolChannel(ctx)
	}
	if chatID == "" {
		chatID = ToolChatID(ctx)
	}

	if channel == "" || chatID == "" {
		return &ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}

	if t.sendCallback == nil {
		return &ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	if err := t.sendCallback(ctx, channel, chatID, content); err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("sending message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	t.tracker.recordDelivered(channel, chatID, content)
	// Silent: user already received the message directly
	result := &ToolResult{
		ForLLM: fmt.Sprintf("Message sent to %s:%s", channel, chatID),
		Silent: true,
	}
	return result.WithTerminal()
}
