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
	for _, toolName := range []string{"web_search", "web_fetch", "fact_check"} {
		if !got.Allows(toolName) {
			t.Fatalf("expected %q to be allowed", toolName)
		}
	}
	if got.Allows("message") {
		t.Fatal("did not expect message tool to be allowed")
	}
}
