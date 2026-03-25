package chatmemory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestEmbedder_EmbedQuery_UsesQueryInputTypeAndExtraBody(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3]}]}`))
	}))
	defer server.Close()

	embedder := NewEmbedder(&config.ModelConfig{
		ModelName: "embedder",
		Model:     "openai/nvidia/llama-nemotron-embed-1b-v2",
		APIBase:   server.URL,
		APIKey:    "test-key",
		ExtraBody: map[string]any{
			"modality": []string{"text"},
			"truncate": "NONE",
		},
	}, config.MemoryEmbeddingConfig{
		Enabled:           true,
		QueryInputType:    "query",
		DocumentInputType: "passage",
	})
	if embedder == nil {
		t.Fatal("expected embedder")
	}

	vector, err := embedder.EmbedQuery(t.Context(), "hello")
	if err != nil {
		t.Fatalf("EmbedQuery() error: %v", err)
	}
	if len(vector) != 3 {
		t.Fatalf("EmbedQuery() vector length = %d, want 3", len(vector))
	}
	if got := captured["input_type"]; got != "query" {
		t.Fatalf("input_type = %#v, want query", got)
	}
	if got := captured["encoding_format"]; got != "float" {
		t.Fatalf("encoding_format = %#v, want float", got)
	}
	if got := captured["truncate"]; got != "NONE" {
		t.Fatalf("truncate = %#v, want NONE", got)
	}
	modality, ok := captured["modality"].([]any)
	if !ok || len(modality) != 1 || modality[0] != "text" {
		t.Fatalf("modality = %#v, want [text]", captured["modality"])
	}
}

func TestEmbedder_EmbedDocuments_UsesPassageInputType(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0]},{"embedding":[0,1]}]}`))
	}))
	defer server.Close()

	embedder := NewEmbedder(&config.ModelConfig{
		ModelName: "embedder",
		Model:     "nvidia/llama-nemotron-embed-1b-v2",
		APIBase:   server.URL,
		APIKey:    "test-key",
	}, config.MemoryEmbeddingConfig{
		Enabled:           true,
		QueryInputType:    "query",
		DocumentInputType: "passage",
	})

	vectors, err := embedder.EmbedDocuments(t.Context(), []string{"doc one", "doc two"})
	if err != nil {
		t.Fatalf("EmbedDocuments() error: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("EmbedDocuments() vectors = %d, want 2", len(vectors))
	}
	if got := captured["input_type"]; got != "passage" {
		t.Fatalf("input_type = %#v, want passage", got)
	}
	modality, ok := captured["modality"].([]any)
	if ok && len(modality) > 0 {
		t.Fatalf("did not expect modality in request without extraBody, got %#v", captured["modality"])
	}
}

func TestNormalizeEmbeddingModel_UsesAPIBaseRules(t *testing.T) {
	if got := normalizeEmbeddingModel("nvidia/llama-nemotron-embed-1b-v2", "https://aio.ooy.cz/api/ai/proxy"); got != "nvidia/llama-nemotron-embed-1b-v2" {
		t.Fatalf("normalizeEmbeddingModel(aio) = %q, want full nvidia-prefixed model", got)
	}
	if got := normalizeEmbeddingModel("nvidia/llama-nemotron-embed-1b-v2", "https://integrate.api.nvidia.com/v1"); got != "llama-nemotron-embed-1b-v2" {
		t.Fatalf("normalizeEmbeddingModel(nvidia official) = %q, want stripped model", got)
	}
	if got := normalizeEmbeddingModel("openrouter/auto", "https://openrouter.ai/api/v1"); got != "auto" {
		t.Fatalf("normalizeEmbeddingModel(openrouter) = %q, want %q", got, "auto")
	}
}

func TestEmbedder_EmbedDocuments_RepeatsSingleModalityForBatch(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0]},{"embedding":[0,1]}]}`))
	}))
	defer server.Close()

	embedder := NewEmbedder(&config.ModelConfig{
		ModelName: "embedder",
		Model:     "nvidia/llama-nemotron-embed-1b-v2",
		APIBase:   server.URL,
		APIKey:    "test-key",
		ExtraBody: map[string]any{
			"modality": []string{"text"},
			"truncate": "NONE",
		},
	}, config.MemoryEmbeddingConfig{
		Enabled:           true,
		QueryInputType:    "query",
		DocumentInputType: "passage",
	})

	_, err := embedder.EmbedDocuments(t.Context(), []string{"doc one", "doc two"})
	if err != nil {
		t.Fatalf("EmbedDocuments() error: %v", err)
	}

	modality, ok := captured["modality"].([]any)
	if !ok || len(modality) != 2 || modality[0] != "text" || modality[1] != "text" {
		t.Fatalf("modality = %#v, want [text text]", captured["modality"])
	}
}
