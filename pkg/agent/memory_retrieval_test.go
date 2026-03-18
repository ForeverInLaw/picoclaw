package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestRetrievalQuery_StripsStructuredEnvelope(t *testing.T) {
	input := "[telegram_group_message]\nsender_label: Сер\n\nнапомни про sqlite память"
	if got := retrievalQuery(input); got != "напомни про sqlite память" {
		t.Fatalf("retrievalQuery() = %q", got)
	}
}

func TestBuildMessages_IncludesRetrievedMemoryBlock(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	msgs := cb.BuildMessages(nil, "", "RETRIEVED_MEMORY: earlier talk about sqlite", "hello", nil, "cli", "direct", "", "")
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	if !strings.Contains(msgs[0].Content, "RETRIEVED_MEMORY: earlier talk about sqlite") {
		t.Fatalf("system prompt missing retrieved memory block: %q", msgs[0].Content)
	}
}

func TestLookupRetrievedMemories_FormatsHits(t *testing.T) {
	dir := t.TempDir()
	idx, err := memoryindex.Open(dir+"\\index.sqlite", memoryindex.Config{
		MaxResults:      3,
		MaxSnippetChars: 120,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	err = idx.AddObservation(t.Context(), memoryindex.Observation{
		SessionKey: "agent:main:telegram:direct:1",
		Channel:    "telegram",
		ChatID:     "1",
		Role:       "user",
		SenderID:   "telegram:1",
		Content:    "мы говорили про sqlite retrieval memory",
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("AddObservation() error: %v", err)
	}

	agent := &AgentInstance{MemoryIndex: idx}
	got := lookupRetrievedMemories(t.Context(), agent, "agent:main:telegram:direct:1", "telegram", "1", "sqlite retrieval")
	if !strings.Contains(got, "RETRIEVED_MEMORY:") {
		t.Fatalf("lookupRetrievedMemories() missing header: %q", got)
	}
	if !strings.Contains(got, "sqlite retrieval memory") {
		t.Fatalf("lookupRetrievedMemories() missing content: %q", got)
	}
}
