package memoryindex

import "testing"

func TestIndex_ListAccessibleChats_UsesParticipantsAndAliasAllowlist(t *testing.T) {
	idx, err := Open(t.TempDir()+"\\index.sqlite", Config{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	if catalogErr := idx.UpsertChatCatalog(t.Context(), ChatCatalogEntry{
		Channel:  "telegram",
		ChatID:   "-1001",
		PeerKind: "group",
		Label:    "Тестовая группа",
	}); catalogErr != nil {
		t.Fatalf("UpsertChatCatalog(group) error: %v", catalogErr)
	}
	if catalogErr := idx.UpsertChatCatalog(t.Context(), ChatCatalogEntry{
		Channel:  "telegram",
		ChatID:   "-1002",
		PeerKind: "group",
		Label:    "Закрытая группа",
	}); catalogErr != nil {
		t.Fatalf("UpsertChatCatalog(closed) error: %v", catalogErr)
	}
	if participantErr := idx.UpsertChatParticipant(t.Context(), ChatParticipantRecord{
		Channel:  "telegram",
		ChatID:   "-1001",
		SenderID: "telegram:42",
		Label:    "Сер",
	}); participantErr != nil {
		t.Fatalf("UpsertChatParticipant() error: %v", participantErr)
	}
	if aliasErr := idx.SyncChatAliases(t.Context(), []ChatAliasRecord{
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
	}); aliasErr != nil {
		t.Fatalf("SyncChatAliases() error: %v", aliasErr)
	}

	participantChats, err := idx.ListAccessibleChats(t.Context(), "telegram:42")
	if err != nil {
		t.Fatalf("ListAccessibleChats(participant) error: %v", err)
	}
	if len(participantChats) != 1 || participantChats[0].ChatID != "-1001" {
		t.Fatalf("participant chats = %#v, want only -1001", participantChats)
	}
	if participantChats[0].Alias != "тестовая группа" {
		t.Fatalf("participant chat alias = %q, want configured alias", participantChats[0].Alias)
	}

	allowedChats, err := idx.ListAccessibleChats(t.Context(), "telegram:77")
	if err != nil {
		t.Fatalf("ListAccessibleChats(allowlist) error: %v", err)
	}
	if len(allowedChats) != 2 {
		t.Fatalf("allowlist chats len = %d, want 2 (%#v)", len(allowedChats), allowedChats)
	}
}
