package anthropicprovider

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// capabilityLookupTimeout bounds the one Models API request made per model.
const capabilityLookupTimeout = 10 * time.Second

// capabilityRetryAfter is how long a failed lookup is remembered before the
// next answer tries again.
const capabilityRetryAfter = 10 * time.Minute

var errMissingCapabilities = errors.New("models API returned no capabilities")

// efforts lists the Messages API effort levels from lowest to highest.
var efforts = []anthropic.OutputConfigEffort{
	anthropic.OutputConfigEffortLow,
	anthropic.OutputConfigEffortMedium,
	anthropic.OutputConfigEffortHigh,
	anthropic.OutputConfigEffortXhigh,
	anthropic.OutputConfigEffortMax,
}

// modelCapabilities is shared by every provider in the process, so pool keys
// and rebuilt providers do not repeat a model's lookup.
var modelCapabilities = newCapabilityCache(time.Now)

// applyThinking maps the agent's thinking_level onto adaptive thinking and
// output_config.effort, within what the model's capabilities allow:
//
//   - "low" … "max": adaptive thinking with that effort, or the nearest effort
//     the model supports.
//   - "adaptive": adaptive thinking with no explicit effort, so the model's
//     default applies.
//   - "off": disabled thinking where the model accepts it, otherwise adaptive
//     thinking with the lowest effort.
//
// budget_tokens is never sent. When the capabilities cannot be read, every
// level but "adaptive" sends adaptive thinking with the lowest effort, which
// every model with effort accepts.
func (p *Provider) applyThinking(
	ctx context.Context,
	params *anthropic.MessageNewParams,
	options map[string]any,
	opts []option.RequestOption,
) {
	level, _ := options["thinking_level"].(string)
	want, ok := levelEffort(level)
	if !ok {
		return
	}
	caps := modelCapabilities.get(ctx, p.client, p.baseURL, params.Model, opts)
	if caps == nil {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		if want != "" {
			params.OutputConfig.Effort = anthropic.OutputConfigEffortLow
		}
		return
	}
	if level == "off" && caps.Thinking.Types.Disabled.Supported {
		disabled := anthropic.NewThinkingConfigDisabledParam()
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &disabled}
		return
	}
	if caps.Thinking.Types.Adaptive.Supported {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
	}
	if want == "" {
		return
	}
	if effort, supported := nearestEffort(caps.Effort, want); supported {
		params.OutputConfig.Effort = effort
	}
}

// levelEffort is the effort a thinking_level asks for: "off" asks for the
// lowest and "adaptive" for none ("").
func levelEffort(level string) (anthropic.OutputConfigEffort, bool) {
	switch level {
	case "off":
		return anthropic.OutputConfigEffortLow, true
	case "adaptive":
		return "", true
	}
	effort := anthropic.OutputConfigEffort(level)
	return effort, slices.Contains(efforts, effort)
}

// nearestEffort picks the supported effort closest to want, preferring a lower
// one; it reports false when the model takes no effort at all.
func nearestEffort(
	caps anthropic.EffortCapability,
	want anthropic.OutputConfigEffort,
) (anthropic.OutputConfigEffort, bool) {
	if !caps.Supported {
		return "", false
	}
	supported := []bool{
		caps.Low.Supported, caps.Medium.Supported, caps.High.Supported, caps.Xhigh.Supported, caps.Max.Supported,
	}
	at := slices.Index(efforts, want)
	for i := at; i >= 0; i-- {
		if supported[i] {
			return efforts[i], true
		}
	}
	for i := at + 1; i < len(efforts); i++ {
		if supported[i] {
			return efforts[i], true
		}
	}
	return "", false
}

// capabilityCache remembers each model's Models API capabilities: a success
// for the life of the process, a failure for capabilityRetryAfter. A lookup
// whose credential Anthropic rejected is not remembered, so one bad key of a
// pool does not decide the model for the others.
type capabilityCache struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*capabilityEntry
}

// capabilityEntry is one model's lookup; its mutex lets a single lookup run
// while concurrent answers for the model wait for it.
type capabilityEntry struct {
	mu      sync.Mutex
	caps    *anthropic.ModelCapabilities
	retryAt time.Time // after a remembered failure, the next lookup's earliest time
}

func newCapabilityCache(now func() time.Time) *capabilityCache {
	return &capabilityCache{now: now, entries: map[string]*capabilityEntry{}}
}

// get returns the model's capabilities, or nil when they are unknown, looking
// them up with the caller's request options (so setup-token requests carry
// their credentials) unless the entry already decides.
func (c *capabilityCache) get(
	ctx context.Context,
	client *anthropic.Client,
	baseURL, model string,
	opts []option.RequestOption,
) *anthropic.ModelCapabilities {
	key := baseURL + "\x00" + model
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		entry = &capabilityEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.caps != nil || c.now().Before(entry.retryAt) {
		return entry.caps
	}
	caps, err := lookupCapabilities(ctx, client, model, opts)
	var apiErr *anthropic.Error
	switch {
	case err == nil:
		entry.caps = caps
	case errors.As(err, &apiErr) &&
		(apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden):
	default:
		entry.retryAt = c.now().Add(capabilityRetryAfter)
	}
	return caps
}

func lookupCapabilities(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	opts []option.RequestOption,
) (*anthropic.ModelCapabilities, error) {
	// The result is shared by other answers, so one answer's cancellation must not decide it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), capabilityLookupTimeout)
	defer cancel()
	info, err := client.Models.Get(ctx, model, anthropic.ModelGetParams{}, opts...)
	if err == nil && !info.JSON.Capabilities.Valid() {
		err = errMissingCapabilities
	}
	if err != nil {
		logger.WarnCF("provider.anthropic", "Model capabilities unavailable; thinking uses the lowest effort",
			map[string]any{"model": model, "error": err.Error()})
		return nil, err
	}
	return &info.Capabilities, nil
}
