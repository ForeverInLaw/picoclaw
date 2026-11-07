package anthropicprovider

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// capabilityLookupTimeout bounds the one Models API request made per model.
const capabilityLookupTimeout = 10 * time.Second

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
var modelCapabilities capabilityCache

// applyThinking maps the agent's thinking_level onto adaptive thinking and
// output_config.effort, within what the model's capabilities allow:
//
//   - "low" … "max": adaptive thinking with that effort, or the nearest effort
//     the model supports.
//   - "adaptive": adaptive thinking with high effort.
//   - "off": disabled thinking where the model accepts it, otherwise adaptive
//     thinking with the lowest effort.
//
// budget_tokens is never sent. When the capabilities cannot be read, the
// requested effort is sent as is and "off" uses the lowest effort.
func (p *Provider) applyThinking(
	ctx context.Context,
	params *anthropic.MessageNewParams,
	options map[string]any,
	opts []option.RequestOption,
) {
	level, _ := options["thinking_level"].(string)
	effort, ok := levelEffort(level)
	if !ok {
		return
	}
	caps := modelCapabilities.get(ctx, p.client, p.baseURL, params.Model, opts)
	if level == "off" && caps != nil && caps.Thinking.Types.Disabled.Supported {
		disabled := anthropic.NewThinkingConfigDisabledParam()
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &disabled}
		return
	}
	if caps == nil || caps.Thinking.Types.Adaptive.Supported {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
	}
	if caps != nil {
		effort, ok = nearestEffort(caps.Effort, effort)
	}
	if ok {
		params.OutputConfig.Effort = effort
	}
}

// levelEffort is the effort a thinking_level asks for; "off" asks for the lowest.
func levelEffort(level string) (anthropic.OutputConfigEffort, bool) {
	switch level {
	case "off":
		return anthropic.OutputConfigEffortLow, true
	case "adaptive":
		return anthropic.OutputConfigEffortHigh, true
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

// capabilityCache remembers each model's Models API capabilities for the life
// of the process, so a model costs one lookup rather than one per answer. A
// failed lookup is remembered as unknown (nil) capabilities.
type capabilityCache struct {
	mu      sync.Mutex
	entries map[string]*capabilityEntry
}

type capabilityEntry struct {
	once sync.Once
	caps *anthropic.ModelCapabilities
}

// get returns the model's capabilities, looking them up on first use with the
// caller's request options (so setup-token requests carry their credentials).
func (c *capabilityCache) get(
	ctx context.Context,
	client *anthropic.Client,
	baseURL, model string,
	opts []option.RequestOption,
) *anthropic.ModelCapabilities {
	key := baseURL + "\x00" + model
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*capabilityEntry)
	}
	entry, ok := c.entries[key]
	if !ok {
		entry = &capabilityEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	entry.once.Do(func() { entry.caps = lookupCapabilities(ctx, client, model, opts) })
	return entry.caps
}

func lookupCapabilities(
	ctx context.Context,
	client *anthropic.Client,
	model string,
	opts []option.RequestOption,
) *anthropic.ModelCapabilities {
	// The result is kept for the process, so one answer's cancellation must not decide it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), capabilityLookupTimeout)
	defer cancel()
	info, err := client.Models.Get(ctx, model, anthropic.ModelGetParams{}, opts...)
	if err == nil && !info.JSON.Capabilities.Valid() {
		err = errMissingCapabilities
	}
	if err != nil {
		logger.WarnCF("provider.anthropic", "Model capabilities unavailable; thinking uses the requested effort",
			map[string]any{"model": model, "error": err.Error()})
		return nil
	}
	return &info.Capabilities
}
