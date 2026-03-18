package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakePlaceholderUpdater struct {
	updates []string
}

func (f *fakePlaceholderUpdater) UpdatePlaceholder(
	ctx context.Context,
	channel, chatID, content string,
) bool {
	f.updates = append(f.updates, channel+"|"+chatID+"|"+content)
	return true
}

func TestClampStreamingPreview_TruncatesLongContent(t *testing.T) {
	got := clampStreamingPreview(strings.Repeat("a", streamPreviewRuneLimit+50))
	if !strings.HasSuffix(got, "\n\n...") {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
	if len([]rune(got)) > streamPreviewRuneLimit {
		t.Fatalf("preview too long: %d", len([]rune(got)))
	}
}

func TestPartialReplyUpdater_ThrottlesAndFlushes(t *testing.T) {
	fake := &fakePlaceholderUpdater{}
	updater := newPartialReplyUpdater(fake, "telegram", "123")
	if updater == nil {
		t.Fatal("expected updater to be created")
	}

	updater.Offer("hello")
	updater.Offer("hello world")
	updater.Flush()

	want := []string{
		"telegram|123|hello",
		"telegram|123|hello world",
	}
	if !slices.Equal(fake.updates, want) {
		t.Fatalf("updates = %#v, want %#v", fake.updates, want)
	}
}

func TestPartialReplyUpdater_DisabledForNonTelegram(t *testing.T) {
	if updater := newPartialReplyUpdater(&fakePlaceholderUpdater{}, "email", "123"); updater != nil {
		t.Fatal("expected nil updater for non-telegram channel")
	}
}

func TestPartialReplyUpdater_DisabledForTypedNilManager(t *testing.T) {
	var fake *fakePlaceholderUpdater
	if updater := newPartialReplyUpdater(fake, "telegram", "123"); updater != nil {
		t.Fatal("expected nil updater for typed-nil manager")
	}
}

func TestPartialReplyUpdater_RespectsThrottleWindow(t *testing.T) {
	fake := &fakePlaceholderUpdater{}
	updater := newPartialReplyUpdater(fake, "telegram", "123")
	if updater == nil {
		t.Fatal("expected updater to be created")
	}

	updater.Offer("hello")
	updater.mu.Lock()
	updater.lastSentAt = time.Now()
	updater.mu.Unlock()
	updater.Offer("hello again")

	if len(fake.updates) != 1 {
		t.Fatalf("expected only one immediate update, got %d", len(fake.updates))
	}

	updater.Flush()
	if len(fake.updates) != 2 {
		t.Fatalf("expected flush to send pending update, got %d", len(fake.updates))
	}
}
