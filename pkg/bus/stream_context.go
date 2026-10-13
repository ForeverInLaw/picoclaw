package bus

import (
	"context"

	"github.com/google/uuid"
)

type deliveryIDKey struct{}

func WithDeliveryID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, deliveryIDKey{}, id)
}

func DeliveryID(ctx context.Context) string {
	id, _ := ctx.Value(deliveryIDKey{}).(string)
	return id
}

func EnsureDeliveryID(ctx context.Context) context.Context {
	if DeliveryID(ctx) == "" {
		return WithDeliveryID(ctx, uuid.NewString())
	}
	return ctx
}

type streamReplyToKey struct{}

// WithStreamReplyTo carries transport reply identity independently from chat routing.
func WithStreamReplyTo(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, streamReplyToKey{}, messageID)
}

func StreamReplyTo(ctx context.Context) string {
	messageID, _ := ctx.Value(streamReplyToKey{}).(string)
	return messageID
}
