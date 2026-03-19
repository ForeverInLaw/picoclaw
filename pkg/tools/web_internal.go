package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/websource"
)

var reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func NewWebSearchProvider(opts WebSearchToolOptions) (SearchProvider, int, error) {
	maxResults := 5
	if opts.PerplexityEnabled && len(opts.PerplexityAPIKeys) > 0 {
		client, err := utils.CreateHTTPClient(opts.Proxy, perplexityTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Perplexity: %w", err)
		}
		provider := &PerplexitySearchProvider{
			keyPool: NewAPIKeyPool(opts.PerplexityAPIKeys),
			proxy:   opts.Proxy,
			client:  client,
		}
		if opts.PerplexityMaxResults > 0 {
			maxResults = opts.PerplexityMaxResults
		}
		return provider, maxResults, nil
	}
	if opts.BraveEnabled && len(opts.BraveAPIKeys) > 0 {
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Brave: %w", err)
		}
		provider := &BraveSearchProvider{keyPool: NewAPIKeyPool(opts.BraveAPIKeys), proxy: opts.Proxy, client: client}
		if opts.BraveMaxResults > 0 {
			maxResults = opts.BraveMaxResults
		}
		return provider, maxResults, nil
	}
	if opts.SearXNGEnabled && opts.SearXNGBaseURL != "" {
		provider := &SearXNGSearchProvider{baseURL: opts.SearXNGBaseURL}
		if opts.SearXNGMaxResults > 0 {
			maxResults = opts.SearXNGMaxResults
		}
		return provider, maxResults, nil
	}
	if opts.TavilyEnabled && len(opts.TavilyAPIKeys) > 0 {
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Tavily: %w", err)
		}
		provider := &TavilySearchProvider{
			keyPool: NewAPIKeyPool(opts.TavilyAPIKeys),
			baseURL: opts.TavilyBaseURL,
			proxy:   opts.Proxy,
			client:  client,
		}
		if opts.TavilyMaxResults > 0 {
			maxResults = opts.TavilyMaxResults
		}
		return provider, maxResults, nil
	}
	if opts.DuckDuckGoEnabled {
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for DuckDuckGo: %w", err)
		}
		provider := &DuckDuckGoSearchProvider{proxy: opts.Proxy, client: client}
		if opts.DuckDuckGoMaxResults > 0 {
			maxResults = opts.DuckDuckGoMaxResults
		}
		return provider, maxResults, nil
	}
	if opts.GLMSearchEnabled && opts.GLMSearchAPIKey != "" {
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for GLM Search: %w", err)
		}
		searchEngine := opts.GLMSearchEngine
		if searchEngine == "" {
			searchEngine = "search_std"
		}
		provider := &GLMSearchProvider{
			apiKey:       opts.GLMSearchAPIKey,
			baseURL:      opts.GLMSearchBaseURL,
			searchEngine: searchEngine,
			proxy:        opts.Proxy,
			client:       client,
		}
		if opts.GLMSearchMaxResults > 0 {
			maxResults = opts.GLMSearchMaxResults
		}
		return provider, maxResults, nil
	}
	return nil, 0, nil
}

func NewWebSearchToolWithProvider(provider SearchProvider, maxResults int) *WebSearchTool {
	if provider == nil {
		return nil
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	return &WebSearchTool{provider: provider, maxResults: maxResults}
}

func renderWebSearchResults(query string, provider SearchProvider, hits []websource.SearchHit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("No results for: %s", query)
	}
	var lines []string
	header := fmt.Sprintf("Results for: %s", query)
	if providerName := strings.TrimSpace(provider.ProviderName()); providerName != "" {
		header = fmt.Sprintf("%s (via %s)", header, providerName)
	}
	lines = append(lines, header)
	for i, hit := range hits {
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, strings.TrimSpace(hit.Title), strings.TrimSpace(hit.URL)))
		if snippet := strings.TrimSpace(hit.Snippet); snippet != "" {
			lines = append(lines, fmt.Sprintf("   %s", snippet))
		}
	}
	return strings.Join(lines, "\n")
}

func (t *WebFetchTool) Fetch(ctx context.Context, urlStr string, maxChars int) (*websource.Document, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("only http/https URLs are allowed")
	}
	if parsedURL.Host == "" {
		return nil, fmt.Errorf("missing domain in URL")
	}
	if isObviousPrivateHost(parsedURL.Hostname(), t.whitelist) {
		return nil, fmt.Errorf("fetching private or local network hosts is not allowed")
	}
	if maxChars <= 100 {
		maxChars = t.maxChars
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.fetchLimitBytes)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, fmt.Errorf("failed to read response: size exceeded %d bytes limit", t.fetchLimitBytes)
		}
		return nil, fmt.Errorf("failed to read response: %v", err)
	}
	bodyStr := string(body)
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		logger.WarnCF("tool", "Failed to parse Content-Type", map[string]any{
			"raw_header": resp.Header.Get("Content-Type"),
			"error":      err.Error(),
		})
		mediaType = "application/octet-stream"
	}
	if charset, ok := params["charset"]; ok && strings.ToLower(charset) != "utf-8" {
		logger.WarnCF("tool", "Note: the content is not in UTF-8", map[string]any{"charset": charset})
	}

	text, extractor, err := extractFetchedText(t, mediaType, body, bodyStr)
	if err != nil {
		return nil, err
	}
	truncated := len(text) > maxChars
	if truncated {
		text = text[:maxChars]
	}
	return &websource.Document{
		URL:       urlStr,
		Status:    resp.StatusCode,
		Extractor: extractor,
		Truncated: truncated,
		Length:    len(text),
		Title:     extractDocumentTitle(mediaType, bodyStr, text),
		LeadText:  extractLeadText(text),
		Text:      text,
	}, nil
}

func (t *WebFetchTool) FetchDocument(ctx context.Context, urlStr string, maxChars int) (*websource.Document, error) {
	return t.Fetch(ctx, urlStr, maxChars)
}

func extractFetchedText(t *WebFetchTool, mediaType string, body []byte, bodyStr string) (string, string, error) {
	switch {
	case mediaType == "application/json":
		var jsonData any
		if err := json.Unmarshal(body, &jsonData); err != nil {
			return bodyStr, "raw", nil
		}
		formatted, err := json.MarshalIndent(jsonData, "", "  ")
		if err != nil {
			return bodyStr, "raw", nil
		}
		return string(formatted), "json", nil
	case mediaType == "text/html" || looksLikeHTML(bodyStr):
		if strings.EqualFold(t.format, "markdown") {
			text, err := utils.HtmlToMarkdown(bodyStr)
			if err != nil {
				return "", "", fmt.Errorf("failed to HTML to markdown: %v", err)
			}
			return text, "markdown", nil
		}
		return t.extractText(bodyStr), "text", nil
	default:
		return bodyStr, "raw", nil
	}
}

func extractDocumentTitle(mediaType, htmlContent, text string) string {
	if mediaType == "text/html" || looksLikeHTML(htmlContent) {
		if matches := reTitle.FindStringSubmatch(htmlContent); len(matches) > 1 {
			return strings.TrimSpace(stripTags(matches[1]))
		}
	}
	return firstNonEmptyLine(text)
}

func extractLeadText(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	collected := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		collected = append(collected, line)
		if len(collected) == 2 {
			break
		}
	}
	return strings.Join(collected, " ")
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
