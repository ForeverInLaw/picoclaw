package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestIsInlineMessage_DetectsMetadataAndTransport(t *testing.T) {
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
				Metadata: map[string]string{telegramInlineMetadataKey: "true"},
			},
			want: true,
		},
		{
			name: "inline transport target",
			msg: bus.InboundMessage{
				Channel: "telegram",
				ChatID:  "inline:abc",
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
			if got := isInlineMessage(tt.msg); got != tt.want {
				t.Fatalf("isInlineMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInlineToolAllowlist_DefaultsToSafeReadOnlyTools(t *testing.T) {
	got := newToolAllowlist(inlineToolAllowlist())
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

func TestInlineResponseQuote_UsesMetadataFallbackAndFormatting(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "inline:abc",
		Content:  "fallback query",
		Metadata: map[string]string{telegramInlineMetadataKey: "true", telegramInlineQueryKey: "quoted query"},
	}

	if got := inlineResponseQuote(msg); got != "quoted query" {
		t.Fatalf("inlineResponseQuote() = %q", got)
	}

	formatted := formatInlineResponse(inlineResponseQuote(msg), "Думаю...")
	want := "> quoted query\n\nДумаю..."
	if formatted != want {
		t.Fatalf("formatInlineResponse() = %q, want %q", formatted, want)
	}
}
