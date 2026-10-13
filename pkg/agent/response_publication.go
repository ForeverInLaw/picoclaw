package agent

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/session"
)

type responsePublicationKey struct{}

// The string response API remains independent of transport, while this request
// context retains the captured conversation lifetime through final bus enqueue.
type responsePublication struct{ guard session.ConversationTurn }

func withResponsePublication(ctx context.Context) context.Context {
	return context.WithValue(bus.EnsureDeliveryID(ctx), responsePublicationKey{}, &responsePublication{})
}

func publicationFor(ctx context.Context) *responsePublication {
	publication, _ := ctx.Value(responsePublicationKey{}).(*responsePublication)
	return publication
}

func (al *AgentLoop) publishResponseWithControls(
	ctx context.Context,
	request bus.InboundMessage,
	channel, chatID, content, replyID string,
	queued *[]bus.InboundMessage,
) {
	if scope, agentID, ok := al.resolveSteeringTarget(request); ok {
		drainCtx, cancel := context.WithCancel(ctx)
		done := make(chan []bus.InboundMessage, 1)
		go func() { done <- al.drainBus(drainCtx, ctx, scope, agentID, len(*queued), false) }()
		defer func() { cancel(); *queued = append(*queued, (<-done)...) }()
	}
	al.publishResponseIfNeeded(ctx, channel, chatID, content, replyID)
}
