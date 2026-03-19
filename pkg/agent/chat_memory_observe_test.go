package agent

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestObserveChatMemoryInbound_StoresGroupChatLabel(t *testing.T) {
	idx, err := memoryindex.Open(filepath.Join(t.TempDir(), "index.sqlite"), memoryindex.Config{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	agent := &AgentInstance{MemoryIndex: idx}
	observeChatMemoryInbound(t.Context(), agent, bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "-1001",
		SenderID: "telegram:42",
		Peer:     bus.Peer{Kind: "group", ID: "-1001"},
		Sender: bus.SenderInfo{
			DisplayName: "Сер",
			Username:    "nevermore",
		},
		Metadata: map[string]string{
			"chat_label":   "Test Group",
			"sender_label": "Сер",
			"is_group":     "true",
		},
	})

	accessible, err := idx.ListAccessibleChats(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("ListAccessibleChats() error: %v", err)
	}
	if len(accessible) != 1 {
		t.Fatalf("accessible chats len = %d, want 1 (%#v)", len(accessible), accessible)
	}
	if accessible[0].Label != "Test Group" {
		t.Fatalf("accessible chat label = %q, want %q", accessible[0].Label, "Test Group")
	}
}
