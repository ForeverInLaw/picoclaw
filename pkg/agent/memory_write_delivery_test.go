package agent

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestRecordDeliveredAssistantMessages_SameTargetToSessionAndMemory(t *testing.T) {
	workspace := t.TempDir()
	store, err := memory.NewJSONLStore(workspace)
	if err != nil {
		t.Fatalf("NewJSONLStore() error: %v", err)
	}
	defer store.Close()

	idx, err := memoryindex.Open(filepath.Join(workspace, "index.sqlite"), memoryindex.Config{
		MaxResults:      5,
		MaxSnippetChars: 200,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	agent := &AgentInstance{
		Sessions:    session.NewJSONLBackend(store),
		MemoryIndex: idx,
	}

	saved := recordDeliveredAssistantMessages(
		t.Context(),
		agent,
		"agent:main:telegram:direct:42",
		"telegram",
		"42",
		[]tools.DeliveredMessage{{Channel: "telegram", ChatID: "42", Content: "visible reply"}},
	)
	if !saved {
		t.Fatal("expected same-target delivery to be saved")
	}

	history := agent.Sessions.GetHistory("agent:main:telegram:direct:42")
	if len(history) != 1 || history[0].Content != "visible reply" {
		t.Fatalf("unexpected history: %#v", history)
	}

	hits, err := idx.Search(t.Context(), memoryindex.SearchRequest{
		Query:      "visible reply",
		SessionKey: "agent:main:telegram:direct:42",
		Channel:    "telegram",
		ChatID:     "42",
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected memory hit for delivered reply")
	}
}
