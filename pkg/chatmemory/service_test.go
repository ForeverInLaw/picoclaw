package chatmemory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestService_Summarize_ResolvesAccessibleGroupByAliasForParticipant(t *testing.T) {
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
	if err := idx.SyncChatAliases(t.Context(), []memoryindex.ChatAliasRecord{{
		Alias:   "test-group",
		Channel: "telegram",
		ChatID:  "-1001",
		Label:   "Тестовая группа",
	}}); err != nil {
		t.Fatalf("SyncChatAliases() error: %v", err)
	}
	if err := idx.AddObservation(t.Context(), memoryindex.Observation{
		SessionKey: "agent:main:telegram:group:-1001",
		Channel:    "telegram",
		ChatID:     "-1001",
		PeerKind:   "group",
		ChatLabel:  "Тестовая группа",
		Role:       "user",
		SenderID:   "telegram:42",
		Content:    "[telegram_group_message]\nsender_label: Сер\n\nОбсуждали alias resolution",
		CreatedAt:  time.Now().UTC().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("AddObservation() error: %v", err)
	}

	service := New(idx, config.MemoryIndexConfig{
		Enabled: true,
		Rollups: config.MemoryRollupConfig{Enabled: true, HourlySampleSize: 4},
	}, nil)
	summary, err := service.Summarize(t.Context(), SummaryRequest{
		RequesterID:    "telegram:42",
		CurrentChannel: "telegram",
		CurrentChatID:  "42",
		CurrentPeer:    "direct",
		Target:         "test-group",
		SinceHours:     24,
	})
	if err != nil {
		t.Fatalf("Summarize() error: %v", err)
	}
	if !strings.Contains(summary, "alias resolution") {
		t.Fatalf("summary missing alias-target content: %q", summary)
	}
}

func TestService_Search_EmbeddingPathRespectsUntilWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := map[string]any{"data": make([]map[string]any, 0, len(payload.Input))}
		for range payload.Input {
			resp["data"] = append(resp["data"].([]map[string]any), map[string]any{"embedding": []float32{1, 0}})
		}
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	idx, err := memoryindex.Open(t.TempDir()+"\\index.sqlite", memoryindex.Config{
		MaxResults:      5,
		MaxSnippetChars: 280,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
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
	for _, obs := range []memoryindex.Observation{
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "old semantic memory hit",
			CreatedAt:  now.Add(-3 * time.Hour),
		},
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "new semantic memory hit",
			CreatedAt:  now.Add(-30 * time.Minute),
		},
	} {
		if err := idx.AddObservation(t.Context(), obs); err != nil {
			t.Fatalf("AddObservation() error: %v", err)
		}
	}

	modelCfg := &config.ModelConfig{
		ModelName: "embedder",
		Model:     "text-embedding-3-small",
		APIBase:   server.URL,
		APIKey:    "test-key",
	}
	embedder := NewEmbedder(modelCfg, config.MemoryEmbeddingConfig{
		Enabled:         true,
		MaxBatch:        8,
		MinContentChars: 1,
	})
	service := New(idx, config.MemoryIndexConfig{
		Enabled:    true,
		MaxResults: 5,
		Embeddings: config.MemoryEmbeddingConfig{
			Enabled:         true,
			ModelName:       "embedder",
			MaxBatch:        8,
			MinContentChars: 1,
		},
	}, embedder)
	service.now = func() time.Time { return now }

	hits, err := service.Search(t.Context(), SearchRequest{
		RequesterID:    "telegram:42",
		CurrentChannel: "telegram",
		CurrentChatID:  "42",
		CurrentPeer:    "direct",
		Target:         "Тестовая группа",
		Query:          "semantic question",
		SinceHours:     24,
		UntilHours:     1,
		Limit:          5,
	})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Content, "old semantic memory hit") {
		t.Fatalf("Search() hits = %#v, want only old hit inside time window", hits)
	}
}

func TestService_StartupBackfill_PopulatesMissingEmbeddingsInBackground(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input     []string `json:"input"`
			InputType string   `json:"input_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.InputType != "passage" {
			t.Fatalf("input_type = %q, want passage", payload.InputType)
		}
		resp := map[string]any{"data": make([]map[string]any, 0, len(payload.Input))}
		for range payload.Input {
			resp["data"] = append(resp["data"].([]map[string]any), map[string]any{"embedding": []float32{1, 0, 0}})
		}
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	idx, err := memoryindex.Open(t.TempDir()+"\\index.sqlite", memoryindex.Config{
		MaxResults:      5,
		MaxSnippetChars: 280,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	for i, content := range []string{"first remembered message", "second remembered message"} {
		if err := idx.AddObservation(t.Context(), memoryindex.Observation{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    content,
			CreatedAt:  time.Now().UTC().Add(-time.Duration(i+1) * time.Minute),
		}); err != nil {
			t.Fatalf("AddObservation() error: %v", err)
		}
	}

	embedder := NewEmbedder(&config.ModelConfig{
		ModelName: "embedder",
		Model:     "nvidia/llama-nemotron-embed-1b-v2",
		APIBase:   server.URL,
		APIKey:    "test-key",
	}, config.MemoryEmbeddingConfig{
		Enabled:                true,
		MaxBatch:               8,
		MinContentChars:        1,
		QueryInputType:         "query",
		DocumentInputType:      "passage",
		StartupBackfillEnabled: true,
	})

	service := New(idx, config.MemoryIndexConfig{
		Enabled: true,
		Embeddings: config.MemoryEmbeddingConfig{
			Enabled:                true,
			ModelName:              "embedder",
			MaxBatch:               8,
			MinContentChars:        1,
			QueryInputType:         "query",
			DocumentInputType:      "passage",
			StartupBackfillEnabled: true,
		},
	}, embedder)
	service.backfillRetryDelay = 10 * time.Millisecond

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		missing, err := idx.ListObservationsMissingEmbeddings(t.Context(), embedder.ModelName(), 8, embedder.MinContentChars())
		if err != nil {
			t.Fatalf("ListObservationsMissingEmbeddings() error: %v", err)
		}
		if len(missing) == 0 {
			rows, err := idx.LoadObservationEmbeddings(t.Context(), []memoryindex.ChatScope{{Channel: "telegram", ChatID: "-1001"}}, embedder.ModelName(), time.Time{}, time.Time{}, 8)
			if err != nil {
				t.Fatalf("LoadObservationEmbeddings() error: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("LoadObservationEmbeddings() rows = %d, want 2", len(rows))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("startup backfill did not complete before deadline")
}

func TestService_StartupBackfill_SkipsShortRecentMessagesAndContinues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := map[string]any{"data": make([]map[string]any, 0, len(payload.Input))}
		for range payload.Input {
			resp["data"] = append(resp["data"].([]map[string]any), map[string]any{"embedding": []float32{1, 0, 0}})
		}
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf.Bytes())
	}))
	defer server.Close()

	idx, err := memoryindex.Open(t.TempDir()+"\\index.sqlite", memoryindex.Config{
		MaxResults:      5,
		MaxSnippetChars: 280,
		MinQueryChars:   3,
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer idx.Close()

	now := time.Now().UTC()
	observations := []memoryindex.Observation{
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "ok",
			CreatedAt:  now.Add(-1 * time.Minute),
		},
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "yo",
			CreatedAt:  now.Add(-2 * time.Minute),
		},
		{
			SessionKey: "agent:main:telegram:group:-1001",
			Channel:    "telegram",
			ChatID:     "-1001",
			PeerKind:   "group",
			ChatLabel:  "Тестовая группа",
			Role:       "user",
			SenderID:   "telegram:42",
			Content:    "this older message is long enough for embeddings",
			CreatedAt:  now.Add(-3 * time.Minute),
		},
	}
	for _, obs := range observations {
		if err := idx.AddObservation(t.Context(), obs); err != nil {
			t.Fatalf("AddObservation() error: %v", err)
		}
	}

	embedder := NewEmbedder(&config.ModelConfig{
		ModelName: "embedder",
		Model:     "nvidia/llama-nemotron-embed-1b-v2",
		APIBase:   server.URL,
		APIKey:    "test-key",
	}, config.MemoryEmbeddingConfig{
		Enabled:                true,
		MaxBatch:               8,
		MinContentChars:        24,
		QueryInputType:         "query",
		DocumentInputType:      "passage",
		StartupBackfillEnabled: true,
	})

	service := New(idx, config.MemoryIndexConfig{
		Enabled: true,
		Embeddings: config.MemoryEmbeddingConfig{
			Enabled:                true,
			ModelName:              "embedder",
			MaxBatch:               8,
			MinContentChars:        24,
			QueryInputType:         "query",
			DocumentInputType:      "passage",
			StartupBackfillEnabled: true,
		},
	}, embedder)
	service.backfillRetryDelay = 10 * time.Millisecond

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := idx.LoadObservationEmbeddings(t.Context(), []memoryindex.ChatScope{{Channel: "telegram", ChatID: "-1001"}}, embedder.ModelName(), time.Time{}, time.Time{}, 8)
		if err != nil {
			t.Fatalf("LoadObservationEmbeddings() error: %v", err)
		}
		if len(rows) == 1 {
			if rows[0].Hit.Content != "this older message is long enough for embeddings" {
				t.Fatalf("embedded wrong observation: %#v", rows[0].Hit.Content)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("startup backfill did not reach older eligible observations")
}
