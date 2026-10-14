package pico

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

// BeginStream creates a transient answer preview. Only Finalize activates the
// shared delivery identity used by ordinary outbound recovery.
func (c *PicoChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	return &picoStreamer{
		channel: c, chatID: chatID, previewID: uuid.NewString(),
		deliveryCtx: bus.EnsureDeliveryID(ctx), replyTo: bus.StreamReplyTo(ctx),
	}, nil
}

type picoStreamer struct {
	mu                         sync.Mutex
	channel                    *PicoChannel
	chatID, previewID, replyTo string
	deliveryCtx                context.Context
	started, finished          bool
	lastSent                   time.Time
}

func (s *picoStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || content == "" || time.Since(s.lastSent) < 100*time.Millisecond {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	kind := TypeMessageUpdate
	if !s.started {
		kind = TypeMessageCreate
	}
	s.started = true
	s.lastSent = time.Now()
	return s.channel.broadcastToSession(s.chatID, newMessage(kind, map[string]any{
		"message_id": s.previewID, "content": content, "final": false,
		"reply_to_message_id": s.replyTo,
	}))
}

func (s *picoStreamer) Finalize(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.finished = true
	return s.channel.broadcastToSession(s.chatID, newMessage(TypeMessageCreate, map[string]any{
		"message_id": bus.DeliveryID(s.deliveryCtx), "replace_message_id": s.previewID,
		"reply_to_message_id": s.replyTo, "content": content, "final": true,
	}))
}

func (s *picoStreamer) Cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	if s.started {
		_ = s.channel.broadcastToSession(
			s.chatID,
			newMessage(TypeMessageDelete, map[string]any{"message_id": s.previewID, "reply_to_message_id": s.replyTo}),
		)
	}
}
