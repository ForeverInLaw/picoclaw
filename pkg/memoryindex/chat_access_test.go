package memoryindex

import "testing"

func TestIndex_ListAccessibleChats_UsesParticipantsAndAliasAllowlist(t *testing.T) {
	idx, err := Open(t.TempDir()+"\\index.sqlite", Config{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if err := idx.UpsertChatCatalog(t.Context(), ChatCatalogEntry{
		Channel:  "telegram",
		ChatID:   "-1001",
		PeerKind: "group",
		Label:    "Тестовая группа",
	}); err != nil {
		t.Fatalf("UpsertChatCatalog(group) error: %v", err)
	}
	if err := idx.UpsertChatCatalog(t.Context(), ChatCatalogEntry{
		Channel:  "telegram",
		ChatID:   "-1002",
		PeerKind: "group",
		Label:    "Закрытая группа",
	}); err != nil {
		t.Fatalf("UpsertChatCatalog(closed) error: %v", err)
	}
	if err := idx.UpsertChatParticipant(t.Context(), ChatParticipantRecord{
		Channel:  "telegram",
		ChatID:   "-1001",
		SenderID: "telegram:42",
		Label:    "Сер",
	}); err != nil {
		t.Fatalf("UpsertChatParticipant() error: %v", err)
	}
	if err := idx.SyncChatAliases(t.Context(), []ChatAliasRecord{
		{
			Alias:             "тестовая группа",
			Channel:           "telegram",
			ChatID:            "-1001",
			Label:             "Тестовая группа",
			AllowedRequesters: []string{"telegram:77"},
		},
		{
			Alias:             "закрытая группа",
			Channel:           "telegram",
			ChatID:            "-1002",
			Label:             "Закрытая группа",
			AllowedRequesters: []string{"telegram:77"},
		},
	}); err != nil {
		t.Fatalf("SyncChatAliases() error: %v", err)
	}

	participantChats, err := idx.ListAccessibleChats(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("ListAccessibleChats(participant) error: %v", err)
	}
	if len(participantChats) != 1 || participantChats[0].ChatID != "-1001" {
		t.Fatalf("participant chats = %#v, want only -1001", participantChats)
	}

	allowedChats, err := idx.ListAccessibleChats(t.Context(), "telegram:77")
	if err != nil {
		t.Fatalf("ListAccessibleChats(allowlist) error: %v", err)
	}
	if len(allowedChats) != 2 {
		t.Fatalf("allowlist chats len = %d, want 2 (%#v)", len(allowedChats), allowedChats)
	}
}
