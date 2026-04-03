package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/chatmemory"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestLookupRetrievedMemories_DirectChatCanRecallAccessibleGroup(t *testing.T) {
	dir := t.TempDir()
	idx, err := memoryindex.Open(filepath.Join(dir, "index.sqlite"), memoryindex.Config{
		MaxResults:      5,
		MaxSnippetChars: 280,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if err := idx.UpsertChatCatalog(t.Context(), memoryindex.ChatCatalogEntry{
		Channel:  "telegram",
		ChatID:   "-1001",
		PeerKind: "group",
		Label:    "Тестовая группа",
	}); err != nil {
		t.Fatalf("UpsertChatCatalog() error: %v", err)
	}
	if err := idx.UpsertChatParticipant(t.Context(), memoryindex.ChatParticipantRecord{
		Channel:  "telegram",
		ChatID:   "-1001",
		SenderID: "telegram:42",
		Label:    "Сер",
	}); err != nil {
		t.Fatalf("UpsertChatParticipant() error: %v", err)
	}
	if err := idx.AddObservation(t.Context(), memoryindex.Observation{
		SessionKey: "agent:main:telegram:group:-1001",
		Channel:    "telegram",
		ChatID:     "-1001",
		PeerKind:   "group",
		ChatLabel:  "Тестовая группа",
		Role:       "user",
		SenderID:   "telegram:42",
		Content:    "[telegram_group_message]\nsender_label: Сер\n\nОбсуждали sqlite retrieval и память группы",
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AddObservation() error: %v", err)
	}

	agent := &AgentInstance{
		MemoryIndex: idx,
		ChatMemory: chatmemory.New(idx, config.MemoryIndexConfig{
			Enabled:         true,
			MaxResults:      5,
			MaxSnippetChars: 280,
			MinQueryChars:   3,
		}, nil),
	}

	got := lookupRetrievedMemories(
		t.Context(),
		agent,
		"agent:main:telegram:direct:42",
		"telegram",
		"42",
		"direct",
		"telegram:42",
		"что мы говорили про sqlite retrieval?",
	)
	if !strings.Contains(got, "sqlite retrieval") || !strings.Contains(got, "-1001") {
		t.Fatalf("lookupRetrievedMemories() = %q, want cross-chat group hit", got)
	}
}
