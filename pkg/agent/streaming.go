package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/constants"
)

const (
	streamUpdateInterval   = 400 * time.Millisecond
	streamPreviewRuneLimit = 3800
	streamUpdateTimeout    = 2 * time.Second
)

type partialReplyUpdater struct {
	manager    placeholderUpdater
	channel    string
	chatID     string
	mu         sync.Mutex
	lastSentAt time.Time
	lastSent   string
	pending    string
	disabled   bool
}

type placeholderUpdater interface {
	UpdatePlaceholder(ctx context.Context, channel, chatID, content string) bool
}

func newPartialReplyUpdater(manager placeholderUpdater, channel, chatID string) *partialReplyUpdater {
	if isNilPlaceholderUpdater(manager) || channel != "telegram" || chatID == "" || constants.IsInternalChannel(channel) {
		return nil
	}
	return &partialReplyUpdater{
		manager: manager,
		channel: channel,
		chatID:  chatID,
	}
}

func (u *partialReplyUpdater) Offer(content string) {
	if u == nil {
		return
	}

	content = clampStreamingPreview(content)
	if content == "" {
		return
	}

	var toSend string

	u.mu.Lock()
	if u.disabled || content == u.lastSent {
		u.mu.Unlock()
		return
	}
	u.pending = content
	if !u.lastSentAt.IsZero() && time.Since(u.lastSentAt) < streamUpdateInterval {
		u.mu.Unlock()
		return
	}
	toSend = u.pending
	u.pending = ""
	u.lastSent = toSend
	u.lastSentAt = time.Now()
	u.mu.Unlock()

	u.apply(toSend)
}

func (u *partialReplyUpdater) Flush() {
	if u == nil {
		return
	}

	var toSend string

	u.mu.Lock()
	if u.disabled || u.pending == "" || u.pending == u.lastSent {
		u.mu.Unlock()
		return
	}
	toSend = u.pending
	u.pending = ""
	u.lastSent = toSend
	u.lastSentAt = time.Now()
	u.mu.Unlock()

	u.apply(toSend)
}

func (u *partialReplyUpdater) apply(content string) {
	if u == nil || isNilPlaceholderUpdater(u.manager) {
		return
	}

	editCtx, cancel := context.WithTimeout(context.Background(), streamUpdateTimeout)
	defer cancel()

	if !u.manager.UpdatePlaceholder(editCtx, u.channel, u.chatID, content) {
		u.mu.Lock()
		u.disabled = true
		u.mu.Unlock()
	}
}

func isNilPlaceholderUpdater(manager placeholderUpdater) bool {
	if manager == nil {
		return true
	}

	value := reflect.ValueOf(manager)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func clampStreamingPreview(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	runes := []rune(content)
	if len(runes) <= streamPreviewRuneLimit {
		return content
	}

	suffix := "\n\n..."
	limit := streamPreviewRuneLimit - len([]rune(suffix))
	if limit < 1 {
		limit = streamPreviewRuneLimit
		suffix = ""
	}
	return strings.TrimSpace(string(runes[:limit])) + suffix
}
