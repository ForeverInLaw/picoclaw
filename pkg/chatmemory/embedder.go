package chatmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type Embedder struct {
	client            *http.Client
	apiBase           string
	apiKey            string
	modelID           string
	modelName         string
	maxBatch          int
	minContentChars   int
	dimensions        int
	queryInputType    string
	documentInputType string
	extraBody         map[string]any
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func NewEmbedder(modelCfg *config.ModelConfig, embeddingCfg config.MemoryEmbeddingConfig) *Embedder {
	if modelCfg == nil || !embeddingCfg.Enabled {
		return nil
	}
	apiBase := strings.TrimRight(strings.TrimSpace(modelCfg.APIBase), "/")
	apiKey := strings.TrimSpace(modelCfg.APIKey)
	if apiBase == "" || apiKey == "" {
		return nil
	}
	_, modelID := providers.ExtractProtocol(modelCfg.Model)
	if modelID == "" {
		modelID = strings.TrimSpace(modelCfg.Model)
	}
	maxBatch := embeddingCfg.MaxBatch
	if maxBatch <= 0 {
		maxBatch = 16
	}
	minChars := embeddingCfg.MinContentChars
	if minChars <= 0 {
		minChars = 24
	}
	timeout := modelCfg.RequestTimeout
	if timeout <= 0 {
		timeout = 60
	}
	return &Embedder{
		client:            &http.Client{Timeout: time.Duration(timeout) * time.Second},
		apiBase:           apiBase,
		apiKey:            apiKey,
		modelID:           modelID,
		modelName:         strings.TrimSpace(modelCfg.ModelName),
		maxBatch:          maxBatch,
		minContentChars:   minChars,
		dimensions:        embeddingCfg.Dimensions,
		queryInputType:    normalizeEmbeddingInputType(embeddingCfg.QueryInputType, "query"),
		documentInputType: normalizeEmbeddingInputType(embeddingCfg.DocumentInputType, "passage"),
		extraBody:         cloneMap(modelCfg.ExtraBody),
	}
}

func (e *Embedder) ModelName() string {
	if e == nil {
		return ""
	}
	return e.modelName
}

func (e *Embedder) MaxBatch() int {
	if e == nil {
		return 0
	}
	return e.maxBatch
}

func (e *Embedder) MinContentChars() int {
	if e == nil {
		return 0
	}
	return e.minContentChars
}

func (e *Embedder) EmbedQuery(ctx context.Context, input string) ([]float32, error) {
	results, err := e.embed(ctx, []string{input}, e.queryInputType)
	if err != nil || len(results) == 0 {
		return nil, err
	}
	return results[0], nil
}

func (e *Embedder) EmbedDocuments(ctx context.Context, inputs []string) ([][]float32, error) {
	return e.embed(ctx, inputs, e.documentInputType)
}

func (e *Embedder) embed(ctx context.Context, inputs []string, inputType string) ([][]float32, error) {
	if e == nil || len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > e.maxBatch {
		inputs = inputs[:e.maxBatch]
	}

	payload := cloneMap(e.extraBody)
	payload["model"] = e.modelID
	payload["input"] = inputs
	payload["encoding_format"] = "float"
	payload["input_type"] = normalizeEmbeddingInputType(inputType, "query")
	if e.dimensions > 0 {
		payload["dimensions"] = e.dimensions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("chatmemory: marshal embeddings request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chatmemory: new embeddings request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatmemory: do embeddings request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("chatmemory: read embeddings response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chatmemory: embeddings status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("chatmemory: decode embeddings response: %w", err)
	}
	out := make([][]float32, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		out = append(out, item.Embedding)
	}
	return out, nil
}

func normalizeEmbeddingInputType(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func cloneMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cosineSimilarity(left, right []float32) float64 {
	if len(left) == 0 || len(right) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, normLeft, normRight float64
	for idx := range left {
		l := float64(left[idx])
		r := float64(right[idx])
		dot += l * r
		normLeft += l * l
		normRight += r * r
	}
	if normLeft == 0 || normRight == 0 {
		return 0
	}
	return dot / (mathSqrt(normLeft) * mathSqrt(normRight))
}

func mathSqrt(value float64) float64 {
	// Newton iteration keeps this helper local and avoids another package dependency in the hot path.
	if value <= 0 {
		return 0
	}
	x := value
	for range 8 {
		x = 0.5 * (x + value/x)
	}
	return x
}
