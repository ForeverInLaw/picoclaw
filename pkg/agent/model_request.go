package agent

import (
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// prepareCandidateRequest starts from the complete tool contract for every
// attempt. A native-search primary must not hide search from another provider.
func prepareCandidateRequest(
	cfg *config.Config,
	client providers.LLMProvider,
	candidate providers.FallbackCandidate,
	definitions []providers.ToolDefinition,
	options map[string]any,
	thinking ThinkingLevel,
) ([]providers.ToolDefinition, map[string]any) {
	candidateOptions := maps.Clone(options)
	if candidateOptions == nil {
		candidateOptions = make(map[string]any)
	}
	delete(candidateOptions, "native_search")
	if mc, err := modelConfigForCandidate(cfg, candidate, ""); err == nil && mc.ThinkingLevel != "" {
		thinking = parseThinkingLevel(mc.ThinkingLevel)
	}
	if configured, ok := options["thinking_level"].(string); ok {
		thinking = parseThinkingLevel(configured)
	}
	delete(candidateOptions, "thinking_level")
	if thinking != ThinkingOff {
		if capable, ok := client.(providers.ThinkingCapable); ok && capable.SupportsThinking() {
			candidateOptions["thinking_level"] = string(thinking)
		}
	}
	hasSearch := false
	for _, definition := range definitions {
		hasSearch = hasSearch || definition.Function.Name == "web_search"
	}
	capable, native := client.(providers.NativeSearchCapable)
	preferNative := cfg.Tools.Web.PreferNative && cfg.Tools.Web.PrimaryProvider == "" &&
		!(cfg.Tools.Web.Parallel.Enabled && len(config.MergeAPIKeys(cfg.Tools.Web.Parallel.APIKey, cfg.Tools.Web.Parallel.APIKeys)) > 0) &&
		!(cfg.Tools.Web.Exa.Enabled && len(config.MergeAPIKeys(cfg.Tools.Web.Exa.APIKey, cfg.Tools.Web.Exa.APIKeys)) > 0)
	if requested, ok := options["native_search"].(bool); ok {
		preferNative = requested
	}
	if !hasSearch || !preferNative || !native || !capable.SupportsNativeSearch() {
		return definitions, candidateOptions
	}
	candidateOptions["native_search"] = true
	filtered := make([]providers.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Function.Name != "web_search" {
			filtered = append(filtered, definition)
		}
	}
	return filtered, candidateOptions
}

// checkCandidateContext never truncates history to accommodate a reserve. The
// ContextManager remains responsible for constructing the turn's Active context.
func checkCandidateContext(
	cfg *config.Config,
	candidate providers.FallbackCandidate,
	client providers.LLMProvider,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	options map[string]any,
	defaultWindow, outputReserve int,
) error {
	var limits *config.ModelCapabilities
	if mc, err := modelConfigForCandidate(cfg, candidate, ""); err == nil {
		limits = mc.Capabilities
	}
	images := false
	for _, message := range messages {
		for _, media := range message.Media {
			parsed, err := url.Parse(media)
			images = images || strings.HasPrefix(media, "data:image/") ||
				err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
		}
	}
	if capable, ok := client.(providers.ImageInputCapable); images && ok && !capable.SupportsImageInput() {
		return &providers.IncompatibleCandidateError{Reason: "provider adapter cannot transmit image input"}
	}
	if limits != nil {
		if images && limits.Images != nil && !*limits.Images {
			return &providers.IncompatibleCandidateError{Reason: "model does not support required image input"}
		}
		requiresTools := len(definitions) > 0 || options["native_search"] == true
		for _, message := range messages {
			requiresTools = requiresTools || len(message.ToolCalls) > 0 || message.Role == "tool"
		}
		if requiresTools && limits.Tools != nil && !*limits.Tools {
			return &providers.IncompatibleCandidateError{Reason: "model does not support required tool contracts"}
		}
		if limits.ContextWindow > 0 {
			defaultWindow = limits.ContextWindow
		}
	}
	if reserve, ok := options["max_tokens"].(int); ok {
		outputReserve = reserve
	}
	if defaultWindow > 0 {
		estimated := estimateRequestTokens(messages, definitions, outputReserve)
		if estimated > defaultWindow {
			return &providers.IncompatibleCandidateError{
				Reason: fmt.Sprintf(
					"Active context requires approximately %d tokens; model context window is %d",
					estimated,
					defaultWindow,
				),
			}
		}
	}
	return nil
}
