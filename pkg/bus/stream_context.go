package bus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type deliveryIDKey struct{}

type deliveryIdentity struct {
	once sync.Once
	id   string
}

// WithDeliveryID resumes an identity already activated for final delivery.
func WithDeliveryID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, deliveryIDKey{}, &deliveryIdentity{id: id})
}

// DeliveryID activates a timestamped UUID only when final delivery first starts.
// Previews retain the shared context without consuming this recovery lifetime.
func DeliveryID(ctx context.Context) string {
	identity, _ := ctx.Value(deliveryIDKey{}).(*deliveryIdentity)
	if identity == nil {
		return ""
	}
	identity.once.Do(func() {
		if identity.id == "" {
			identity.id = uuid.Must(uuid.NewV7()).String()
		}
	})
	return identity.id
}

func EnsureDeliveryID(ctx context.Context) context.Context {
	if _, ok := ctx.Value(deliveryIDKey{}).(*deliveryIdentity); !ok {
		return context.WithValue(ctx, deliveryIDKey{}, &deliveryIdentity{})
	}
	return ctx
}

// DeliveryStartedAt remains verifiable after a transport cursor is pruned.
// Fragment identities retain the original UUID before their ':part:' suffix.
func DeliveryStartedAt(id string) (time.Time, error) {
	base, _, _ := strings.Cut(id, ":")
	parsed, err := uuid.Parse(base)
	if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return time.Time{}, fmt.Errorf("invalid timestamped delivery identity")
	}
	sec, nsec := parsed.Time().UnixTime()
	return time.Unix(sec, nsec), nil
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
