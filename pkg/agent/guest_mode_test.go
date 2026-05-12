package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestIsGuestMessage_DetectsMetadataAndTransport(t *testing.T) {
	tests := []struct {
		name string
		msg  bus.InboundMessage
		want bool
	}{
		{
			name: "metadata marker",
			msg: bus.InboundMessage{
				Channel:  "telegram",
				ChatID:   "123",
				Metadata: map[string]string{telegramGuestMetadataKey: "true"},
			},
			want: true,
		},
		{
			name: "guest transport target",
			msg: bus.InboundMessage{
				Channel: "telegram",
				ChatID:  "guest:abc",
			},
			want: true,
		},
		{
			name: "normal telegram direct",
			msg: bus.InboundMessage{
				Channel: "telegram",
				ChatID:  "123",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGuestMessage(tt.msg); got != tt.want {
				t.Fatalf("isGuestMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGuestToolAllowlist_DefaultsToSafeReadOnlyTools(t *testing.T) {
	got := newToolAllowlist(guestToolAllowlist())
	for _, toolName := range []string{"web_search", "web_fetch", "fact_check", "exec", "read_file", "write_file", "list_dir", "cron"} {
		if !got.Allows(toolName) {
			t.Fatalf("expected %q to be allowed", toolName)
		}
	}
	if got.Allows("message") {
		t.Fatal("did not expect message tool to be allowed")
	}
	if got.Allows("send_messages") {
		t.Fatal("did not expect send_messages tool to be allowed")
	}
}

func TestGuestResponseQuote_UsesMetadataFallbackAndFormatting(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "guest:abc",
		Content:  "fallback query",
		Metadata: map[string]string{telegramGuestMetadataKey: "true", telegramGuestQueryKey: "quoted query"},
	}

	if got := guestResponseQuote(msg); got != "quoted query" {
		t.Fatalf("guestResponseQuote() = %q", got)
	}

	formatted := formatGuestResponse(guestResponseQuote(msg), "Думаю...")
	want := "> quoted query\n\nДумаю..."
	if formatted != want {
		t.Fatalf("formatGuestResponse() = %q, want %q", formatted, want)
	}
}
