package bus

import "context"

type streamReplyToKey struct{}

// WithStreamReplyTo carries transport reply identity independently from chat routing.
func WithStreamReplyTo(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, streamReplyToKey{}, messageID)
}

func StreamReplyTo(ctx context.Context) string {
	messageID, _ := ctx.Value(streamReplyToKey{}).(string)
	return messageID
}
