package factcheck

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/sipeed/picoclaw/pkg/websource"
)

const (
	defaultSearchCount = 6
	defaultFetchChars  = 12000
	maxSources         = 4
)

var (
	reSentenceSplit = regexp.MustCompile(`[.!?]\s+`)
	reNonWord       = regexp.MustCompile(`[^a-z0-9]+`)
	stopWords       = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
		"for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {},
		"that": {}, "the": {}, "to": {}, "was": {}, "were": {}, "will": {}, "with": {},
	}
	contradictionCues = []string{
		"false", "not true", "incorrect", "did not", "didn't", "never happened",
		"denied", "debunk", "debunked", "no evidence", "unfounded", "misleading",
	}
	supportCues = []string{
		"announced", "confirmed", "official", "reported", "released", "published",
		"according to", "states that", "said that",
	}
)

type Service struct {
	searcher Searcher
	fetcher  Fetcher
}

func NewService(searcher Searcher, fetcher Fetcher) *Service {
	return &Service{searcher: searcher, fetcher: fetcher}
}

func (s *Service) Check(ctx context.Context, req Request) (Result, error) {
	if s == nil || s.searcher == nil || s.fetcher == nil {
		return Result{}, fmt.Errorf("fact check service is not configured")
	}

	claim := strings.TrimSpace(req.Claim)
	sourceURL := strings.TrimSpace(req.URL)
	question := strings.TrimSpace(req.Question)

	var originalHost string
	if sourceURL != "" {
		doc, err := s.fetcher.Fetch(ctx, sourceURL, defaultFetchChars)
		if err != nil {
			return Result{}, fmt.Errorf("failed to fetch source URL: %w", err)
		}
		if claim == "" {
			claim = deriveClaimFromDocument(doc)
		}
		if parsed, err := url.Parse(sourceURL); err == nil {
			originalHost = strings.ToLower(parsed.Hostname())
		}
	}
	if claim == "" {
		return Result{}, fmt.Errorf("claim or url is required")
	}

	query := buildQuery(claim, question)
	hits, err := s.searcher.Search(ctx, query, defaultSearchCount)
	if err != nil {
		return Result{}, fmt.Errorf("search failed: %w", err)
	}
	candidates := selectCandidates(hits, sourceURL)
	analyses := make([]documentAnalysis, 0, len(candidates))
	for _, hit := range candidates {
		doc, err := s.fetcher.Fetch(ctx, hit.URL, defaultFetchChars)
		if err != nil {
			continue
		}
		analyses = append(analyses, analyzeDocument(claim, originalHost, hit, doc))
	}

	result := buildResult(claim, analyses)
	if len(result.Sources) == 0 && sourceURL != "" {
		result.Sources = append(result.Sources, Source{
			Title:      sourceURL,
			URL:        sourceURL,
			SourceType: "primary",
		})
	}
	return result, nil
}

type documentAnalysis struct {
	hit           websource.SearchHit
	sourceType    string
	keywordHits   int
	hasSupportCue bool
	hasContraCue  bool
}

func analyzeDocument(claim, originalHost string, hit websource.SearchHit, doc *websource.Document) documentAnalysis {
	text := normalizeText(strings.Join([]string{doc.Title, doc.LeadText, doc.Text, hit.Snippet}, " "))
	keywords := extractKeywords(claim)
	keywordHits := 0
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			keywordHits++
		}
	}
	return documentAnalysis{
		hit:           hit,
		sourceType:    classifySource(hit.URL, originalHost),
		keywordHits:   keywordHits,
		hasSupportCue: containsAny(text, supportCues),
		hasContraCue:  containsAny(text, contradictionCues),
	}
}

func buildResult(claim string, analyses []documentAnalysis) Result {
	if len(analyses) == 0 {
		return Result{
			Verdict:   VerdictUnverified,
			Claim:     claim,
			Rationale: "Не нашёл достаточно надёжных источников для проверки утверждения.",
		}
	}

	supportCount, contradictionCount := 0, 0
	primarySupport, primaryContradiction := 0, 0
	sources := make([]Source, 0, maxSources)

	for _, analysis := range analyses {
		if analysis.keywordHits == 0 {
			continue
		}
		if len(sources) < maxSources {
			sources = append(sources, Source{
				Title:      analysis.hit.Title,
				URL:        analysis.hit.URL,
				SourceType: analysis.sourceType,
			})
		}
		if analysis.hasContraCue && analysis.keywordHits >= 2 {
			contradictionCount++
			if analysis.sourceType == "primary" {
				primaryContradiction++
			}
			continue
		}
		if analysis.keywordHits >= 2 && analysis.hasSupportCue {
			supportCount++
			if analysis.sourceType == "primary" {
				primarySupport++
			}
		}
	}

	switch {
	case primarySupport > 0 && contradictionCount == 0:
		return Result{
			Verdict:   VerdictSupported,
			Claim:     claim,
			Rationale: "Нашёл подтверждение в первичном источнике и не увидел надёжных опровержений.",
			Sources:   sources,
		}
	case primaryContradiction > 0 && supportCount == 0:
		return Result{
			Verdict:   VerdictContradicted,
			Claim:     claim,
			Rationale: "Первичный источник противоречит утверждению.",
			Sources:   sources,
		}
	case supportCount >= 2 && contradictionCount == 0:
		return Result{
			Verdict:   VerdictSupported,
			Claim:     claim,
			Rationale: "Несколько независимых источников подтверждают основной тезис.",
			Sources:   sources,
		}
	case contradictionCount >= 2 && supportCount == 0:
		return Result{
			Verdict:   VerdictContradicted,
			Claim:     claim,
			Rationale: "Несколько независимых источников противоречат основному тезису.",
			Sources:   sources,
		}
	case supportCount > 0 && contradictionCount > 0:
		return Result{
			Verdict:   VerdictMixed,
			Claim:     claim,
			Rationale: "Источники расходятся: часть подтверждает тезис, часть ему противоречит.",
			Sources:   sources,
		}
	default:
		return Result{
			Verdict:   VerdictUnverified,
			Claim:     claim,
			Rationale: "Данных недостаточно: упоминания есть, но подтверждение слабое или неочевидное.",
			Sources:   sources,
		}
	}
}

func buildQuery(claim, question string) string {
	if question == "" {
		return claim
	}
	return strings.TrimSpace(claim + " " + question)
}

func deriveClaimFromDocument(doc *websource.Document) string {
	if doc == nil {
		return ""
	}
	if title := strings.TrimSpace(doc.Title); title != "" {
		if lead := firstSentence(doc.LeadText); lead != "" {
			return strings.TrimSpace(title + ". " + lead)
		}
		return title
	}
	if lead := firstSentence(doc.LeadText); lead != "" {
		return lead
	}
	return firstSentence(doc.Text)
}

func selectCandidates(hits []websource.SearchHit, sourceURL string) []websource.SearchHit {
	selected := make([]websource.SearchHit, 0, maxSources)
	seen := make(map[string]struct{})
	if sourceURL != "" {
		selected = append(selected, websource.SearchHit{Title: sourceURL, URL: sourceURL})
		seen[sourceURL] = struct{}{}
	}
	for _, hit := range hits {
		if strings.TrimSpace(hit.URL) == "" {
			continue
		}
		if _, ok := seen[hit.URL]; ok {
			continue
		}
		if isFilteredDomain(hit.URL) {
			continue
		}
		selected = append(selected, hit)
		seen[hit.URL] = struct{}{}
		if len(selected) >= maxSources {
			break
		}
	}
	return selected
}

func classifySource(rawURL, originalHost string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "secondary"
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case originalHost != "" && host == originalHost:
		return "primary"
	case strings.HasSuffix(host, ".gov"), strings.HasSuffix(host, ".mil"), strings.HasSuffix(host, ".edu"):
		return "primary"
	case strings.Contains(host, "docs."), strings.Contains(host, "developer."), strings.Contains(host, "support."):
		return "primary"
	default:
		return "secondary"
	}
}

func isFilteredDomain(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	filtered := []string{
		"google.com", "duckduckgo.com", "bing.com", "yahoo.com", "yandex.",
		"facebook.com", "instagram.com", "t.co", "x.com", "twitter.com",
	}
	return slices.ContainsFunc(filtered, func(pattern string) bool {
		return strings.Contains(host, pattern)
	})
}

func extractKeywords(text string) []string {
	normalized := normalizeText(text)
	parts := strings.Fields(normalized)
	keywords := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		if _, stop := stopWords[part]; stop {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		keywords = append(keywords, part)
	}
	return keywords
}

func normalizeText(text string) string {
	return strings.TrimSpace(reNonWord.ReplaceAllString(strings.ToLower(text), " "))
}

func containsAny(text string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parts := reSentenceSplit.Split(text, 2)
	return strings.TrimSpace(parts[0])
}
