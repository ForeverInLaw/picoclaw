package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/websource"
)

const searchExcerptChars = 1500

// ParallelSearchProvider uses the GA Search API; provider options stay in this adapter.
type ParallelSearchProvider struct {
	keyPool *APIKeyPool
	baseURL string
	client  *http.Client
}

func (p *ParallelSearchProvider) ProviderName() string { return "Parallel" }

func (p *ParallelSearchProvider) Search(ctx context.Context, query string, count int) ([]websource.SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	count = max(1, min(count, 10))
	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.parallel.ai"
	}
	request := struct {
		SearchQueries    []string `json:"search_queries"`
		Mode             string   `json:"mode"`
		MaxCharsTotal    int      `json:"max_chars_total"`
		AdvancedSettings struct {
			MaxResults      int `json:"max_results"`
			ExcerptSettings struct {
				MaxCharsPerResult int `json:"max_chars_per_result"`
			} `json:"excerpt_settings"`
		} `json:"advanced_settings"`
	}{SearchQueries: []string{query}, Mode: "fast", MaxCharsTotal: count * searchExcerptChars}
	request.AdvancedSettings.MaxResults = count
	request.AdvancedSettings.ExcerptSettings.MaxCharsPerResult = searchExcerptChars
	var response struct {
		Results []struct {
			Title    string   `json:"title"`
			URL      string   `json:"url"`
			Excerpts []string `json:"excerpts"`
		} `json:"results"`
	}
	if err := searchJSONWithKeys(
		ctx,
		p.client,
		baseURL+"/v1/search",
		p.keyPool,
		p.ProviderName(),
		request,
		&response,
	); err != nil {
		return nil, err
	}
	hits := make([]websource.SearchHit, 0, min(count, len(response.Results)))
	for _, result := range response.Results {
		if strings.TrimSpace(result.URL) == "" {
			continue
		}
		hits = append(
			hits,
			websource.SearchHit{
				Title:    result.Title,
				URL:      result.URL,
				Snippet:  boundedSearchExcerpt(strings.Join(result.Excerpts, "\n")),
				Provider: p.ProviderName(),
			},
		)
		if len(hits) == count {
			break
		}
	}
	return hits, nil
}

// searchHTTPError preserves status without returning a provider body that could echo credentials.
type searchHTTPError struct {
	provider string
	status   int
	quota    bool
	auth     bool
}

func (e *searchHTTPError) Error() string {
	if e.quota {
		return fmt.Sprintf("%s search quota or credits exhausted (HTTP %d)", e.provider, e.status)
	}
	return fmt.Sprintf("%s search returned HTTP %d", e.provider, e.status)
}

func searchJSON(
	ctx context.Context,
	client *http.Client,
	endpoint, apiKey, provider string,
	payload, result any,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s request: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s search: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		message := strings.ToLower(string(body))
		quota := resp.StatusCode == http.StatusPaymentRequired
		for _, marker := range []string{"insufficient_quota", "quota_exceeded", "credits exhausted", "insufficient credits", "exceeded your current quota", "payment_required", "credit balance"} {
			if strings.Contains(message, marker) {
				quota = true
				break
			}
		}
		auth := resp.StatusCode == http.StatusUnauthorized
		if resp.StatusCode == http.StatusForbidden {
			for _, marker := range []string{"invalid_api_key", "invalid api key", "expired api key", "revoked api key", "authentication_failed", "authentication_error"} {
				if strings.Contains(message, marker) {
					auth = true
					break
				}
			}
		}
		return &searchHTTPError{provider: provider, status: resp.StatusCode, quota: quota, auth: auth}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode %s search: %w", provider, err)
	}
	return nil
}

func boundedSearchExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > searchExcerptChars {
		return string(runes[:searchExcerptChars]) + "… [truncated]"
	}
	return string(runes)
}
