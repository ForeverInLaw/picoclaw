package pico

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

func (c *PicoChannel) SupportsReasoningProgress() bool { return true }

// SendProgress preserves the agent's independent action identity and public
// result contract. It never edits an answer or advances final delivery.
func (c *PicoChannel) SendProgress(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	if msg.Progress == nil {
		return nil
	}
	p := msg.Progress
	event := newMessage(TypeActivityUpdate, map[string]any{
		"message_id": fmt.Sprintf("activity:%s:%s", p.RequestID, p.ActionID),
		"request_id": p.RequestID, "action_id": p.ActionID,
		"reply_to_message_id": msg.ReplyToMessageID,
		"kind":                p.Kind, "state": p.State, "tool": p.Tool,
		"arguments": p.Arguments, "result": p.Result, "error": p.Error,
	})
	return c.broadcastToSession(msg.ChatID, event)
}
