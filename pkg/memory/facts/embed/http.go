package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPProvider calls an OpenAI-compatible /embeddings endpoint. Works with
// NVIDIA NIM, vLLM, llama.cpp, official OpenAI, and any other gateway that
// follows the same wire format:
//
//	POST {APIBase}/embeddings
//	Authorization: Bearer {APIKey}
//	Content-Type: application/json
//	{"input":[text], "model":"<model>", "encoding_format":"float"}
//	→ {"data":[{"embedding":[float, ...]}]}
type HTTPProvider struct {
	APIBase string        // e.g. "https://integrate.api.nvidia.com/v1"
	APIKey  string        // bearer token
	Model   string        // provider-specific model id, e.g. "nvidia/llama-nemotron-embed-1b-v2"
	Timeout time.Duration // optional, default 30s

	// InputType is sent as "input_type" when non-empty. NVIDIA distinguishes
	// "query" and "passage". OpenAI ignores the field.
	InputType string

	// ExtraBody is merged into the request body. Used for provider-specific
	// flags like NVIDIA's "truncate":"NONE".
	ExtraBody map[string]any

	client *http.Client
}

type embeddingsRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
	InputType      string   `json:"input_type,omitempty"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed sends a single-text embedding request and returns the resulting
// vector. The model parameter is ignored — HTTPProvider is bound to a model
// at construction.
func (p *HTTPProvider) Embed(ctx context.Context, _, text string) ([]float32, error) {
	if strings.TrimSpace(p.APIBase) == "" || strings.TrimSpace(p.APIKey) == "" {
		return nil, fmt.Errorf("embed: HTTPProvider not configured")
	}
	if p.client == nil {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		p.client = &http.Client{Timeout: timeout}
	}

	// PicoClaw's model_list entries prefix the model id with a protocol
	// marker ("openai/..." for any OpenAI-compatible endpoint). The remote
	// service expects the bare model id, so strip the marker.
	model := strings.TrimPrefix(p.Model, "openai/")
	body := map[string]any{
		"input":           []string{text},
		"model":           model,
		"encoding_format": "float",
	}
	if p.InputType != "" {
		body["input_type"] = p.InputType
	}
	for k, v := range p.ExtraBody {
		body[k] = v
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}

	url := strings.TrimRight(p.APIBase, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("embed: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("embed: HTTP %d: %s", resp.StatusCode, snippet)
	}

	var out embeddingsResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty response")
	}
	return out.Data[0].Embedding, nil
}
