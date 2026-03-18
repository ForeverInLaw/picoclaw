package chatmemory

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestService_Summarize_ResolvesAccessibleGroupByLabel(t *testing.T) {
	idx, err := memoryindex.Open(t.TempDir()+"\\index.sqlite", memoryindex.Config{
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

	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	for _, obs := range []memoryindex.Observation{
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "[telegram_group_message]\nsender_label: Сер\n\nОбсуждали memory index и сводки по чату",
			CreatedAt:  now.Add(-2 * time.Hour),
		},
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:77",
			Content:    "[telegram_group_message]\nsender_label: Визард\n\nНужно чтобы бот помнил обсуждение за день",
			CreatedAt:  now.Add(-90 * time.Minute),
		},
	} {
		if err := idx.AddObservation(t.Context(), obs); err != nil {
			t.Fatalf("AddObservation() error: %v", err)
		}
	}

	service := New(idx, config.MemoryIndexConfig{
		Enabled: true,
		Rollups: config.MemoryRollupConfig{
			Enabled:          true,
			HourlySampleSize: 6,
		},
	}, nil)
	service.now = func() time.Time { return now }

	summary, err := service.Summarize(t.Context(), SummaryRequest{
		RequesterID:    "telegram:42",
		CurrentChannel: "telegram",
		CurrentChatID:  "42",
		CurrentPeer:    "direct",
		Target:         "тестовая группа",
		SinceHours:     24,
	})
	if err != nil {
		t.Fatalf("Summarize() error: %v", err)
	}
	if !strings.Contains(summary, "Тестовая группа") {
		t.Fatalf("summary missing target label: %q", summary)
	}
	if !strings.Contains(summary, "memory index") || !strings.Contains(summary, "помнил обсуждение") {
		t.Fatalf("summary missing excerpts: %q", summary)
	}
}
