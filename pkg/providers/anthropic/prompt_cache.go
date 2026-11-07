package anthropicprovider

import "github.com/anthropics/anthropic-sdk-go"

// promptCache places Anthropic's prompt-cache points on a request: one explicit
// breakpoint closing the static prefix (tools and the static system part) and
// automatic top-level caching of the growing conversation tail. Both carry the
// same TTL, so the rule that longer-TTL entries come first always holds.
type promptCache struct {
	ttl anthropic.CacheControlEphemeralTTL // zero means the API default of 5 minutes
}

func (c promptCache) apply(params *anthropic.MessageNewParams) {
	marker := anthropic.NewCacheControlEphemeralParam()
	marker.TTL = c.ttl

	// buildParams marks the system blocks the caller declared cacheable; the last
	// one ends the static prefix, and the dynamic blocks after it stay uncached.
	staticEnd := -1
	for i := range params.System {
		if params.System[i].CacheControl.Type != "" {
			staticEnd = i
			params.System[i].CacheControl = anthropic.CacheControlEphemeralParam{}
		}
	}
	switch {
	case staticEnd >= 0:
		params.System[staticEnd].CacheControl = marker
	case len(params.Tools) > 0:
		// Without a declared static system part, the tool definitions are the prefix.
		if last := params.Tools[len(params.Tools)-1].GetCacheControl(); last != nil {
			*last = marker
		}
	}
	params.CacheControl = marker
}
