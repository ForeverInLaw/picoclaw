package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/chatmemory"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestChatMemoryTool_ExecuteSummaryCurrentChat(t *testing.T) {
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
	if err := idx.AddObservation(t.Context(), memoryindex.Observation{
		SessionKey: "agent:main:telegram:group:-1001",
		Channel:    "telegram",
		ChatID:     "-1001",
		PeerKind:   "group",
		ChatLabel:  "Тестовая группа",
		Role:       "user",
		SenderID:   "telegram:42",
		Content:    "[telegram_group_message]\nsender_label: Сер\n\nГоворили про chat memory tool",
		CreatedAt:  time.Now().UTC().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("AddObservation() error: %v", err)
	}

	service := chatmemory.New(idx, config.MemoryIndexConfig{
		Enabled: true,
		Rollups: config.MemoryRollupConfig{
			Enabled:          true,
			HourlySampleSize: 4,
		},
	}, nil)
	tool := NewChatMemoryTool(service)
	ctx := WithToolSender(WithToolContext(t.Context(), "telegram", "-1001"), bus.SenderInfo{
		PlatformID:  "42",
		CanonicalID: "telegram:42",
	})

	result := tool.Execute(ctx, map[string]any{
		"mode":        "summary",
		"chat":        "current",
		"since_hours": 24,
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "CHAT_MEMORY_SUMMARY") || !strings.Contains(result.ForLLM, "chat memory tool") {
		t.Fatalf("Execute() = %q, want summary content", result.ForLLM)
	}
}

func TestChatMemoryTool_ExecuteSearch_HonorsExplicitLimit(t *testing.T) {
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
	now := time.Now().UTC()
	for _, content := range []string{
		"[telegram_group_message]\nsender_label: Сер\n\nchat memory result alpha",
		"[telegram_group_message]\nsender_label: Сер\n\nchat memory result beta",
	} {
		if err := idx.AddObservation(t.Context(), memoryindex.Observation{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    content,
			CreatedAt:  now,
		}); err != nil {
			t.Fatalf("AddObservation() error: %v", err)
		}
		now = now.Add(time.Minute)
	}

	service := chatmemory.New(idx, config.MemoryIndexConfig{
		Enabled:    true,
		MaxResults: 5,
	}, nil)
	tool := NewChatMemoryTool(service)
	ctx := WithToolSender(WithToolContext(t.Context(), "telegram", "-1001"), bus.SenderInfo{
		PlatformID:  "42",
		CanonicalID: "telegram:42",
	})

	result := tool.Execute(ctx, map[string]any{
		"mode":        "search",
		"chat":        "current",
		"query":       "chat memory result",
		"since_hours": 24,
		"limit":       1,
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}
	if strings.Count(result.ForLLM, "channel=telegram chat=-1001 role=user") != 1 {
		t.Fatalf("Execute() returned more than 1 hit: %q", result.ForLLM)
	}
}
