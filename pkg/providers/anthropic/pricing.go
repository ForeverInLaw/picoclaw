package anthropicprovider

import (
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// tokenPrice is a model's first-party Claude API price in dollars per million
// tokens.
type tokenPrice struct {
	input, cacheWrite5m, cacheWrite1h, cacheRead, output float64
}

// modelPrices is the built-in price table, keyed by API model id.
// Source: https://platform.claude.com/docs/en/about-claude/pricing, retrieved 2026-11-07.
// Cache reads cost 0.1x input, except 0.025x on Fable 5.1 and Mythos 5.1 and
// 0.05x on Opus 5.5 and Sonnet 5.5. Haiku 5.5 has a second tier, haiku55LongPrompt.
var modelPrices = map[anthropic.Model]tokenPrice{
	"claude-fable-5-1":  {10, 12.50, 20, 0.25, 50},
	"claude-mythos-5-1": {10, 12.50, 20, 0.25, 50},
	"claude-fable-5":    {10, 12.50, 20, 1, 50},
	"claude-mythos-5":   {10, 12.50, 20, 1, 50},
	"claude-opus-5-5":   {4, 5, 8, 0.20, 20},
	"claude-opus-5":     {5, 6.25, 10, 0.50, 25},
	"claude-opus-4-8":   {5, 6.25, 10, 0.50, 25},
	"claude-opus-4-7":   {5, 6.25, 10, 0.50, 25},
	"claude-opus-4-6":   {5, 6.25, 10, 0.50, 25},
	"claude-opus-4-5":   {5, 6.25, 10, 0.50, 25},
	"claude-sonnet-5-5": {2, 2.50, 4, 0.10, 10},
	"claude-sonnet-5":   {2, 2.50, 4, 0.20, 10},
	"claude-sonnet-4-6": {3, 3.75, 6, 0.30, 15},
	"claude-sonnet-4-5": {3, 3.75, 6, 0.30, 15},
	"claude-haiku-5-5":  {0.10, 0.125, 0.20, 0.01, 0.50},
	"claude-haiku-4-5":  {1, 1.25, 2, 0.10, 5},
}

// haiku55LongPrompt prices a whole Haiku 5.5 request whose prompt, cache reads
// and writes included, is over haiku55TierTokens.
var haiku55LongPrompt = tokenPrice{0.50, 0.625, 1, 0.05, 2.50}

const haiku55TierTokens = 100_000

// datedSnapshot matches the date suffix of pre-4.6 snapshot ids such as
// claude-haiku-4-5-20251001, which share their alias's price.
var datedSnapshot = regexp.MustCompile(`-\d{8}$`)

// apiModelID is the Messages API id of a configured model id: the API uses
// hyphens (claude-sonnet-4-6) where configuration may use dots (claude-sonnet-4.6).
func apiModelID(model string) anthropic.Model {
	return anthropic.Model(strings.ReplaceAll(model, ".", "-"))
}

// EstimateCost prices one answer's usage for the model that served it, with
// cache writes at cacheTTL ("1h", or "5m"/"" for 5 minutes). ok is false for
// a model missing from the price table, which gets no guessed amount.
func EstimateCost(model string, usage UsageInfo, cacheTTL string) (usd float64, ok bool) {
	id := apiModelID(datedSnapshot.ReplaceAllString(model, ""))
	price, ok := modelPrices[id]
	if !ok {
		return 0, false
	}
	if id == anthropic.ModelClaudeHaiku5_5 && usage.PromptTokens > haiku55TierTokens {
		price = haiku55LongPrompt
	}
	cacheWrite := price.cacheWrite5m
	if cacheTTL == string(anthropic.CacheControlEphemeralTTLTTL1h) {
		cacheWrite = price.cacheWrite1h
	}
	perMTok := float64(usage.UncachedPromptTokens())*price.input +
		float64(usage.CacheCreationTokens)*cacheWrite +
		float64(usage.CacheReadTokens)*price.cacheRead +
		float64(usage.CompletionTokens)*price.output
	return perMTok / 1e6, true
}
