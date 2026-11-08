package anthropicprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type (
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
)

const (
	defaultBaseURL      = "https://api.anthropic.com"
	anthropicBetaHeader = "oauth-2025-04-20"
)

type Provider struct {
	client      *anthropic.Client
	tokenSource func(context.Context) (string, error)
	baseURL     string
	// requestTimeout bounds non-streaming requests; streams rely on the
	// caller's context and inactivity tracking instead.
	requestTimeout time.Duration
	// promptCache is nil for setup-token providers, whose requests stay as they were.
	promptCache *promptCache
	// serverFallback opts supporting models into Anthropic's server-side refusal fallback.
	serverFallback bool
	// keyLabel names the Key pool key in usage logs; empty outside pools.
	keyLabel string
}

// SupportsThinking implements providers.ThinkingCapable.
func (p *Provider) SupportsThinking() bool { return true }

func NewProvider(token string) *Provider {
	return NewProviderWithBaseURL(token, "")
}

func NewProviderWithBaseURL(token, apiBase string) *Provider {
	baseURL := normalizeBaseURL(apiBase)
	client := anthropic.NewClient(
		// Credentials come from configuration only, never from env vars or SDK profiles.
		option.WithoutEnvironmentDefaults(),
		option.WithAuthToken(token),
		option.WithMaxRetries(0),
		option.WithBaseURL(baseURL),
	)
	return &Provider{
		client:  &client,
		baseURL: baseURL,
	}
}

// NewProviderWithAPIKey authenticates with an Anthropic API key (x-api-key)
// rather than a bearer token. A zero requestTimeout uses the shared default.
func NewProviderWithAPIKey(apiKey, apiBase, proxy string, requestTimeout time.Duration) *Provider {
	if requestTimeout <= 0 {
		requestTimeout = common.DefaultRequestTimeout
	}
	baseURL := normalizeBaseURL(apiBase)
	// The SDK applies requestTimeout per request; a client-wide timeout would cut long streams.
	httpClient := &http.Client{Transport: common.NewHTTPClient(proxy).Transport}
	client := anthropic.NewClient(
		// Credentials come from configuration only, never from env vars or SDK profiles.
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(httpClient),
	)
	return &Provider{
		client:         &client,
		baseURL:        baseURL,
		requestTimeout: requestTimeout,
		promptCache:    &promptCache{},
		serverFallback: true,
	}
}

// WithPromptCacheTTL sets the lifetime of both prompt-cache points: "1h", or
// "5m"/"" for Anthropic's 5-minute default. Providers that do not cache ignore it.
func (p *Provider) WithPromptCacheTTL(ttl string) *Provider {
	if p.promptCache != nil {
		p.promptCache.ttl = anthropic.CacheControlEphemeralTTL(ttl)
	}
	return p
}

// WithKeyLabel names the Key pool key this provider authenticates with in its
// usage logs. The label is never the secret.
func (p *Provider) WithKeyLabel(label string) *Provider {
	p.keyLabel = label
	return p
}

func NewProviderWithClient(client *anthropic.Client) *Provider {
	return &Provider{
		client:  client,
		baseURL: defaultBaseURL,
	}
}

func NewProviderWithTokenSource(token string, tokenSource func() (string, error)) *Provider {
	return NewProviderWithTokenSourceAndBaseURL(token, tokenSource, "")
}

func NewProviderWithTokenSourceAndBaseURL(token string, tokenSource func() (string, error), apiBase string) *Provider {
	p := NewProviderWithBaseURL(token, apiBase)
	p.tokenSource = func(context.Context) (string, error) { return tokenSource() }
	return p
}

// NewProviderWithContextTokenSource uses the model attempt's cancellation for credential acquisition.
func NewProviderWithContextTokenSource(
	token string,
	source func(context.Context) (string, error),
	apiBase string,
) *Provider {
	p := NewProviderWithBaseURL(token, apiBase)
	p.tokenSource = source
	return p
}

func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if p.tokenSource != nil {
		tok, err := p.tokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		opts = append(opts,
			option.WithAuthToken(tok),
			option.WithHeader("anthropic-beta", anthropicBetaHeader),
		)
	}

	params, err := p.requestParams(ctx, messages, tools, model, options, opts)
	if err != nil {
		return nil, err
	}
	opts = append(opts, p.serverFallbackOptions(params.Model)...)

	// OAuth/setup-tokens require streaming; API keys use non-streaming.
	if p.tokenSource != nil {
		return p.chatStreaming(ctx, params, opts, nil)
	}

	if p.requestTimeout > 0 {
		opts = append(opts, option.WithRequestTimeout(p.requestTimeout))
	}
	resp, err := p.client.Messages.New(ctx, params, opts...)
	if err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	return p.completeResponse(params.Model, resp), nil
}

func (p *Provider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(content string),
) (*LLMResponse, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if p.tokenSource != nil {
		tok, err := p.tokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		opts = append(opts,
			option.WithAuthToken(tok),
			option.WithHeader("anthropic-beta", anthropicBetaHeader),
		)
	}

	params, err := p.requestParams(ctx, messages, tools, model, options, opts)
	if err != nil {
		return nil, err
	}
	opts = append(opts, p.serverFallbackOptions(params.Model)...)

	return p.chatStreaming(ctx, params, opts, onUpdate)
}

func (p *Provider) chatStreaming(
	ctx context.Context,
	params anthropic.MessageNewParams,
	opts []option.RequestOption,
	onUpdate func(content string),
) (*LLMResponse, error) {
	stream := p.client.Messages.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var msg anthropic.Message
	var partial strings.Builder
	completed := false
	for stream.Next() {
		event := stream.Current()
		if event.Type == "content_block_delta" {
			delta := event.AsContentBlockDelta().Delta
			if (delta.Type == "text_delta" && delta.Text != "") ||
				(delta.Type == "thinking_delta" && delta.Thinking != "") ||
				(delta.Type == "input_json_delta" && delta.PartialJSON != "") {
				protocoltypes.ReportModelProgress(ctx)
			}
			if delta.Type == "text_delta" && delta.Text != "" && onUpdate != nil {
				partial.WriteString(delta.Text)
				onUpdate(partial.String())
			}
		}
		if event.Type == "content_block_start" {
			block := event.AsContentBlockStart().ContentBlock
			if block.Text != "" || block.Thinking != "" ||
				((block.Type == "tool_use" || block.Type == "server_tool_use") && (block.ID != "" || block.Name != "")) {
				protocoltypes.ReportModelProgress(ctx)
			}
		}
		if err := msg.Accumulate(event); err != nil {
			return nil, fmt.Errorf("claude streaming accumulate: %w", err)
		}
		if event.Type == "message_delta" {
			mergeDeltaUsage(&msg.Usage, event.AsMessageDelta().Usage)
		}
		if event.Type == "message_stop" {
			completed = true
			break
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	if !completed {
		return nil, fmt.Errorf("claude stream ended before message_stop: %w", io.ErrUnexpectedEOF)
	}
	return p.completeResponse(params.Model, &msg), nil
}

// mergeDeltaUsage applies the cumulative counts of a message_delta event. The
// SDK's Accumulate copies only output_tokens, which would drop the final input
// and cache counts.
func mergeDeltaUsage(usage *anthropic.Usage, delta anthropic.MessageDeltaUsage) {
	if delta.JSON.InputTokens.Valid() {
		usage.InputTokens = delta.InputTokens
	}
	if delta.JSON.CacheCreationInputTokens.Valid() {
		usage.CacheCreationInputTokens = delta.CacheCreationInputTokens
	}
	if delta.JSON.CacheReadInputTokens.Valid() {
		usage.CacheReadInputTokens = delta.CacheReadInputTokens
	}
}

// completeResponse logs a finished message's usage against the model that
// served it, naming the requested model too when they differ (for example after
// a server-side fallback), and maps the message to an LLMResponse.
func (p *Provider) completeResponse(requested anthropic.Model, msg *anthropic.Message) *LLMResponse {
	served := servedModel(msg)
	fields := map[string]any{
		"model":                       served,
		"input_tokens":                msg.Usage.InputTokens,
		"output_tokens":               msg.Usage.OutputTokens,
		"cache_creation_input_tokens": msg.Usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     msg.Usage.CacheReadInputTokens,
	}
	if p.keyLabel != "" {
		fields["key"] = p.keyLabel
	}
	resp := parseResponse(msg)
	if served != requested {
		fields["requested_model"] = requested
		resp.ServedModel = served
	}
	logger.InfoCF("provider.anthropic", "Anthropic usage", fields)
	return resp
}

func (p *Provider) GetDefaultModel() string {
	return "claude-sonnet-4.6"
}

func (p *Provider) BaseURL() string {
	return p.baseURL
}

// requestParams builds the Messages API request, maps thinking_level for the
// model and, for providers that cache, places the prompt-cache points on it.
// opts are the answer's request options, reused for the capability lookup.
func (p *Provider) requestParams(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	opts []option.RequestOption,
) (anthropic.MessageNewParams, error) {
	params, err := buildParams(messages, tools, model, options)
	if err != nil {
		return params, err
	}
	p.applyThinking(ctx, &params, options, opts)
	if p.promptCache != nil {
		p.promptCache.apply(&params)
	}
	return params, nil
}

func buildParams(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (anthropic.MessageNewParams, error) {
	var system []anthropic.TextBlockParam
	var anthropicMessages []anthropic.MessageParam

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Prefer structured SystemParts for per-block cache_control.
			// This enables LLM-side KV cache reuse: the static block's prefix
			// hash stays stable across requests while dynamic parts change freely.
			if len(msg.SystemParts) > 0 {
				for _, part := range msg.SystemParts {
					block := anthropic.TextBlockParam{Text: part.Text}
					if part.CacheControl != nil && part.CacheControl.Type == "ephemeral" {
						block.CacheControl = anthropic.NewCacheControlEphemeralParam()
					}
					system = append(system, block)
				}
			} else {
				system = append(system, anthropic.TextBlockParam{Text: msg.Content})
			}
		case "user":
			if msg.ToolCallID != "" {
				anthropicMessages = append(anthropicMessages,
					anthropic.NewUserMessage(anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false)),
				)
			} else {
				anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(userContent(msg)...))
			}
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var blocks []anthropic.ContentBlockParamUnion
				if msg.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
				}
				for _, tc := range msg.ToolCalls {
					// Stored history keeps the name only in Function.Name.
					name := tc.Name
					if name == "" && tc.Function != nil {
						name = tc.Function.Name
					}
					// Skip tool calls with empty names to avoid API errors
					if name == "" {
						continue
					}
					args := tc.Arguments
					if args == nil && tc.Function != nil && tc.Function.Arguments != "" {
						if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
							args = map[string]any{}
						}
					}
					if args == nil {
						args = map[string]any{}
					}
					blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, args, name))
				}
				anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(blocks...))
			} else {
				anthropicMessages = append(anthropicMessages,
					anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)),
				)
			}
		case "tool":
			anthropicMessages = append(anthropicMessages,
				anthropic.NewUserMessage(anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false)),
			)
		}
	}

	maxTokens := int64(4096)
	if mt, ok := options["max_tokens"].(int); ok {
		maxTokens = int64(mt)
	}

	params := anthropic.MessageNewParams{
		Model:     apiModelID(model),
		Messages:  anthropicMessages,
		MaxTokens: maxTokens,
	}

	if len(system) > 0 {
		params.System = system
	}

	// temperature is never sent: the current API version rejects it on every model.

	if len(tools) > 0 {
		params.Tools = translateTools(tools)
	}

	return params, nil
}

// userContent renders a user message's text followed by its image media.
// Media the Messages API cannot take as an image (e.g. audio) is skipped, and
// an image-only message carries no empty text block, which the API rejects.
func userContent(msg Message) []anthropic.ContentBlockParamUnion {
	var blocks []anthropic.ContentBlockParamUnion
	for _, media := range msg.Media {
		if header, data, ok := strings.Cut(strings.TrimPrefix(media, "data:"), ","); ok &&
			strings.HasPrefix(media, "data:image/") && strings.HasSuffix(header, ";base64") {
			blocks = append(blocks, anthropic.NewImageBlockBase64(strings.TrimSuffix(header, ";base64"), data))
			continue
		}
		if parsed, err := url.Parse(media); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
			parsed.Hostname() != "" {
			blocks = append(blocks, anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: media}))
		}
	}
	if msg.Content != "" || len(blocks) == 0 {
		blocks = append([]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(msg.Content)}, blocks...)
	}
	return blocks
}

func translateTools(tools []ToolDefinition) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool := anthropic.ToolParam{
			Name: t.Function.Name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: t.Function.Parameters["properties"],
			},
		}
		if desc := t.Function.Description; desc != "" {
			tool.Description = anthropic.String(desc)
		}
		if req, ok := t.Function.Parameters["required"].([]any); ok {
			required := make([]string, 0, len(req))
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
			tool.InputSchema.Required = required
		}
		result = append(result, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return result
}

func parseResponse(resp *anthropic.Message) *LLMResponse {
	if resp.StopReason == anthropic.StopReasonRefusal {
		return refusalResponse(resp)
	}
	var content strings.Builder
	var reasoning strings.Builder
	toolCalls := make([]ToolCall, 0, 1)

	for _, block := range resp.Content {
		switch block.Type {
		case "thinking":
			tb := block.AsThinking()
			reasoning.WriteString(tb.Thinking)
		case "text":
			tb := block.AsText()
			content.WriteString(tb.Text)
		case "fallback":
			// The fallback model continues the declined model's text; thinking and
			// tool calls from before the handoff are not part of the answer.
			reasoning.Reset()
			toolCalls = toolCalls[:0]
		case "tool_use":
			tu := block.AsToolUse()
			var args map[string]any
			if err := json.Unmarshal(tu.Input, &args); err != nil {
				log.Printf("anthropic: failed to decode tool call input for %q: %v", tu.Name, err)
				args = map[string]any{"raw": string(tu.Input)}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:        tu.ID,
				Name:      tu.Name,
				Arguments: args,
			})
		}
	}

	finishReason := "stop"
	switch resp.StopReason {
	case anthropic.StopReasonToolUse:
		finishReason = "tool_calls"
	case anthropic.StopReasonMaxTokens:
		finishReason = "length"
	case anthropic.StopReasonEndTurn:
		finishReason = "stop"
	}

	return &LLMResponse{
		Content:      content.String(),
		Reasoning:    reasoning.String(),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usageInfo(resp.Usage),
	}
}

// usageInfo counts the whole prompt: Anthropic's input_tokens covers only the
// tokens after the last cache breakpoint.
func usageInfo(usage anthropic.Usage) *UsageInfo {
	prompt := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	return &UsageInfo{
		PromptTokens:        int(prompt),
		CompletionTokens:    int(usage.OutputTokens),
		TotalTokens:         int(prompt + usage.OutputTokens),
		CacheCreationTokens: int(usage.CacheCreationInputTokens),
		CacheReadTokens:     int(usage.CacheReadInputTokens),
	}
}

func normalizeBaseURL(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return defaultBaseURL
	}

	base = strings.TrimRight(base, "/")
	if before, ok := strings.CutSuffix(base, "/v1"); ok {
		base = before
	}
	if base == "" {
		return defaultBaseURL
	}

	return base
}
