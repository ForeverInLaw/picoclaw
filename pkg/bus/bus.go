package bus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// ErrBusClosed is returned when publishing to a closed MessageBus.
var ErrBusClosed = errors.New("message bus closed")

const defaultBusBufferSize = 64

// StreamDelegate is implemented by the channel Manager to provide streaming
// capabilities to the agent loop without tight coupling.
type StreamDelegate interface {
	// GetStreamer returns a Streamer for the given channel+chatID if the channel
	// supports streaming. Returns nil, false if streaming is unavailable.
	GetStreamer(ctx context.Context, channel, chatID string) (Streamer, bool)
}

// Streamer pushes incremental content to a streaming-capable channel.
// Defined here so the agent loop can use it without importing pkg/channels.
type Streamer interface {
	Update(ctx context.Context, content string) error
	Finalize(ctx context.Context, content string) error
	Cancel(ctx context.Context)
}

type MessageBus struct {
	inbound       chan InboundMessage
	outbound      chan OutboundMessage
	outboundMedia chan OutboundMediaMessage

	closeOnce      sync.Once
	done           chan struct{}
	publishMu      sync.Mutex // Registration and shutdown share a gate before wg.Wait.
	closed         bool
	wg             sync.WaitGroup
	streamDelegate atomic.Value // stores StreamDelegate
}

func NewMessageBus() *MessageBus {
	return &MessageBus{
		inbound:       make(chan InboundMessage, defaultBusBufferSize),
		outbound:      make(chan OutboundMessage, defaultBusBufferSize),
		outboundMedia: make(chan OutboundMediaMessage, defaultBusBufferSize),
		done:          make(chan struct{}),
	}
}

func publish[T any](ctx context.Context, mb *MessageBus, ch chan T, msg T) error {
	mb.publishMu.Lock()
	if mb.closed {
		mb.publishMu.Unlock()
		return ErrBusClosed
	}
	if err := ctx.Err(); err != nil {
		mb.publishMu.Unlock()
		return err
	}
	mb.wg.Add(1)
	mb.publishMu.Unlock()
	defer mb.wg.Done()

	select {
	case ch <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	}
}

func (mb *MessageBus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	return publish(ctx, mb, mb.inbound, msg)
}

func (mb *MessageBus) InboundChan() <-chan InboundMessage {
	return mb.inbound
}

func (mb *MessageBus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	return publish(ctx, mb, mb.outbound, msg)
}

// PublishOutboundGuarded commits one enqueue through a lifetime gate. The gate
// holds its lock only for a nonblocking attempt; backpressure waits outside it.
// A revoked gate returns false without publishing. Shutdown shares publishMu.
func (mb *MessageBus) PublishOutboundGuarded(
	ctx context.Context, msg OutboundMessage, commit func(func() error) (bool, error),
) (bool, error) {
	full := errors.New("outbound queue full")
	for {
		current, err := commit(func() error {
			mb.publishMu.Lock()
			defer mb.publishMu.Unlock()
			if mb.closed {
				return ErrBusClosed
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case mb.outbound <- msg:
				return nil
			default:
				return full
			}
		})
		if !errors.Is(err, full) {
			return current && err == nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-mb.done:
			timer.Stop()
			return false, ErrBusClosed
		case <-timer.C:
		}
	}
}

func (mb *MessageBus) OutboundChan() <-chan OutboundMessage {
	return mb.outbound
}

func (mb *MessageBus) PublishOutboundMedia(ctx context.Context, msg OutboundMediaMessage) error {
	return publish(ctx, mb, mb.outboundMedia, msg)
}

func (mb *MessageBus) OutboundMediaChan() <-chan OutboundMediaMessage {
	return mb.outboundMedia
}

// SetStreamDelegate registers a StreamDelegate (typically the channel Manager).
func (mb *MessageBus) SetStreamDelegate(d StreamDelegate) {
	mb.streamDelegate.Store(d)
}

// GetStreamer returns a Streamer for the given channel+chatID via the delegate.
func (mb *MessageBus) GetStreamer(ctx context.Context, channel, chatID string) (Streamer, bool) {
	if d, ok := mb.streamDelegate.Load().(StreamDelegate); ok && d != nil {
		return d.GetStreamer(ctx, channel, chatID)
	}
	return nil, false
}

func (mb *MessageBus) Close() {
	mb.closeOnce.Do(func() {
		// Stop registration before waiting; a publisher that passed an atomic
		// flag check alone could otherwise call Add after Wait reached zero.
		mb.publishMu.Lock()
		mb.closed = true
		close(mb.done)
		mb.publishMu.Unlock()

		// wait for all ongoing Publish calls to finish, ensuring all messages have been sent to channels or exited
		mb.wg.Wait()

		// close channels safely
		close(mb.inbound)
		close(mb.outbound)
		close(mb.outboundMedia)

		// clean up any remaining messages in channels
		drained := 0
		for range mb.inbound {
			drained++
		}
		for range mb.outbound {
			drained++
		}
		for range mb.outboundMedia {
			drained++
		}

		if drained > 0 {
			logger.DebugCF("bus", "Drained buffered messages during close", map[string]any{
				"count": drained,
			})
		}
	})
}
