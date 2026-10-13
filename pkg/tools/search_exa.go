package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/websource"
)

// ExaSearchProvider keeps Exa's request and excerpt format behind SearchProvider.
type ExaSearchProvider struct {
	keyPool *APIKeyPool
	baseURL string
	client  *http.Client
}

func (p *ExaSearchProvider) ProviderName() string { return "Exa" }

func (p *ExaSearchProvider) Search(ctx context.Context, query string, count int) ([]websource.SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	count = max(1, min(count, 10))
	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.exa.ai"
	}
	request := struct {
		Query      string `json:"query"`
		Type       string `json:"type"`
		NumResults int    `json:"numResults"`
		Contents   struct {
			Highlights struct {
				MaxCharacters int `json:"maxCharacters"`
			} `json:"highlights"`
		} `json:"contents"`
	}{Query: query, Type: "auto", NumResults: count}
	request.Contents.Highlights.MaxCharacters = searchExcerptChars
	var response struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Highlights []string `json:"highlights"`
			Text       string   `json:"text"`
		} `json:"results"`
	}
	if err := searchJSONWithKeys(
		ctx,
		p.client,
		baseURL+"/search",
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
		snippet := strings.Join(result.Highlights, "\n")
		if strings.TrimSpace(snippet) == "" {
			snippet = result.Text
		}
		hits = append(
			hits,
			websource.SearchHit{
				Title:    result.Title,
				URL:      result.URL,
				Snippet:  boundedSearchExcerpt(snippet),
				Provider: p.ProviderName(),
			},
		)
		if len(hits) == count {
			break
		}
	}
	return hits, nil
}
