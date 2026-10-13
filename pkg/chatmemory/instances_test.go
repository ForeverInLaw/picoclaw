package chatmemory

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestNamedInstanceArchiveRetrievalKeepsCanonicalPersonAndScope(t *testing.T) {
	idx, err := memoryindex.Open(filepath.Join(t.TempDir(), "memory.sqlite"), memoryindex.Config{MinQueryChars: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	for _, channel := range []string{"telegram", "work"} {
		if catalogErr := idx.UpsertChatCatalog(
			t.Context(),
			memoryindex.ChatCatalogEntry{Channel: channel, ChatID: "-1", PeerKind: "group", Label: channel},
		); catalogErr != nil {
			t.Fatal(catalogErr)
		}
		if participantErr := idx.UpsertChatParticipant(
			t.Context(),
			memoryindex.ChatParticipantRecord{Channel: channel, ChatID: "-1", SenderID: "telegram:42"},
		); participantErr != nil {
			t.Fatal(participantErr)
		}
		if observationErr := idx.AddObservation(
			t.Context(),
			memoryindex.Observation{
				Channel:   channel,
				ChatID:    "-1",
				PeerKind:  "group",
				SenderID:  "telegram:42",
				Role:      "user",
				Content:   "isolation marker " + channel,
				CreatedAt: time.Now(),
			},
		); observationErr != nil {
			t.Fatal(observationErr)
		}
	}
	service := New(
		idx,
		config.MemoryIndexConfig{MaxResults: 5, MinQueryChars: 3},
		nil,
		config.ChannelList{"work": {Type: "telegram"}},
	)
	for _, channel := range []string{"telegram", "work"} {
		req := SearchRequest{
			RequesterID:    "telegram:42",
			CurrentChannel: channel,
			CurrentChatID:  "42",
			CurrentPeer:    "direct",
			Query:          "isolation marker",
		}
		hits, searchErr := service.Search(t.Context(), req)
		if searchErr != nil || len(hits) != 1 || hits[0].Channel != channel {
			t.Fatalf("%s retrieval crossed instance: %#v %v", channel, hits, searchErr)
		}
		req.Target = "work"
		if channel == "work" {
			req.Target = "telegram"
		}
		if _, targetErr := service.Search(t.Context(), req); targetErr == nil {
			t.Fatalf("%s explicitly accessed another instance", channel)
		}
	}
	// Removing a named instance from configuration must not grant access to its
	// historical archive through the default instance's canonical person ID.
	service = New(idx, config.MemoryIndexConfig{MaxResults: 5, MinQueryChars: 3}, nil)
	hits, err := service.Search(
		t.Context(),
		SearchRequest{
			RequesterID:    "telegram:42",
			CurrentChannel: "telegram",
			CurrentChatID:  "42",
			CurrentPeer:    "direct",
			Query:          "isolation marker",
		},
	)
	if err != nil || len(hits) != 1 || hits[0].Channel != "telegram" {
		t.Fatalf("removed instance archive crossed boundary: %#v %v", hits, err)
	}
}
