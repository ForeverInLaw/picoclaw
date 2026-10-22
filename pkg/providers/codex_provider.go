package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

const (
	codexDefaultModel        = "gpt-5.3-codex"
	codexDefaultInstructions = "You are Codex, a coding assistant."
)

type CodexProvider struct {
	registered         bool
	contextTokenSource func(context.Context) (string, string, error)
	client             *openai.Client
	accountID          string
	tokenSource        func() (string, string, error)
	enableWebSearch    bool
}

const defaultCodexInstructions = "You are Codex, a coding assistant."

func NewCodexProvider(token, accountID string) *CodexProvider {
	opts := []option.RequestOption{
		option.WithBaseURL("https://chatgpt.com/backend-api/codex"),
		option.WithMaxRetries(0),
		option.WithAPIKey(token),
		option.WithHeader("originator", "codex_cli_rs"),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	}
	client := openai.NewClient(opts...)
	return &CodexProvider{
		client:          &client,
		accountID:       accountID,
		enableWebSearch: true,
	}
}

func NewCodexProviderWithTokenSource(
	token, accountID string, tokenSource func() (string, string, error),
) *CodexProvider {
	p := NewCodexProvider(token, accountID)
	p.tokenSource = tokenSource
	return p
}

func (p *CodexProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	return p.chat(ctx, messages, tools, model, options, nil)
}

func (p *CodexProvider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(content string),
) (*LLMResponse, error) {
	return p.chat(ctx, messages, tools, model, options, func(update StreamContent) {
		if onUpdate != nil && (update.Phase == ContentPhaseUnknown || update.Phase == ContentPhaseAnswer) {
			onUpdate(update.Content)
		}
	})
}

func (p *CodexProvider) ChatStreamContent(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(StreamContent),
) (*LLMResponse, error) {
	return p.chat(ctx, messages, tools, model, options, onUpdate)
}

func (p *CodexProvider) chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(StreamContent),
) (*LLMResponse, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	accountID := p.accountID
	resolvedModel, fallbackReason := model, ""
	if !p.registered {
		resolvedModel, fallbackReason = resolveCodexModel(model)
	}
	if fallbackReason != "" {
		logger.WarnCF(
			"provider.codex",
			"Requested model is not compatible with Codex backend, using fallback",
			map[string]any{
				"requested_model": model,
				"resolved_model":  resolvedModel,
				"reason":          fallbackReason,
			},
		)
	}
	if p.contextTokenSource != nil || p.tokenSource != nil {
		var tok, accID string
		var err error
		if p.contextTokenSource != nil {
			tok, accID, err = p.contextTokenSource(ctx)
		} else {
			tok, accID, err = p.tokenSource()
		}
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		opts = append(opts, option.WithAPIKey(tok))
		if accID != "" {
			accountID = accID
		}
	}
	if accountID != "" && !p.registered {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	} else if !p.registered {
		logger.WarnCF(
			"provider.codex",
			"No account id found for Codex request; backend may reject with 400",
			map[string]any{
				"requested_model": model,
				"resolved_model":  resolvedModel,
			},
		)
	}

	// Respect tools.web.prefer_native: only inject native search when the agent
	// loop requested it (options["native_search"]), so prefer_native: false
	useNativeSearch := p.enableWebSearch && (options["native_search"] == true)
	params := buildCodexParams(messages, tools, resolvedModel, options, useNativeSearch)
	if p.registered {
		// The public ChatGPT-plan route accepts local functions inside a namespace.
		// Reuse existing history translation and streaming response parsing.
		var functions []responses.FunctionToolParam
		var namespaced []any
		for _, tool := range params.Tools {
			if tool.OfFunction != nil {
				functions = append(functions, *tool.OfFunction)
			} else {
				namespaced = append(namespaced, tool)
			}
		}
		if len(functions) > 0 {
			namespaced = append(namespaced, map[string]any{"type": "namespace", "name": "picoclaw", "description": "Picoclaw local tools", "tools": functions})
		}
		if len(namespaced) > 0 {
			opts = append(opts, option.WithJSONSet("tools", namespaced))
		}
		for i := range params.Input.OfInputItemList {
			if call := params.Input.OfInputItemList[i].OfFunctionCall; call != nil {
				call.SetExtraFields(map[string]any{"namespace": "picoclaw"})
			}
		}
	}

	var transportResponse *http.Response
	opts = append(opts, option.WithResponseInto(&transportResponse))
	stream := p.client.Responses.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var resp *responses.Response
	var terminalError error
	type contentPosition struct{ output, content int64 }
	partials := make(map[contentPosition]*strings.Builder)
	phases := make(map[int64]ContentPhase)
	for stream.Next() {
		evt := stream.Current()
		if evt.Type == "response.output_item.added" || evt.Type == "response.output_item.done" {
			phases[evt.OutputIndex] = codexMessagePhase(evt.Item)
		}
		if evt.Type == "response.output_text.delta" {
			deltaEvent := evt.AsResponseOutputTextDelta()
			if deltaEvent.Delta != "" && onUpdate != nil {
				position := contentPosition{deltaEvent.OutputIndex, deltaEvent.ContentIndex}
				partial := partials[position]
				if partial == nil {
					partial = &strings.Builder{}
					partials[position] = partial
				}
				partial.WriteString(deltaEvent.Delta)
				phase := phases[deltaEvent.OutputIndex]
				positions := make([]contentPosition, 0, len(partials))
				for pos := range partials {
					if phases[pos.output] == phase {
						positions = append(positions, pos)
					}
				}
				slices.SortFunc(positions, func(a, b contentPosition) int {
					if a.output < b.output || (a.output == b.output && a.content < b.content) {
						return -1
					}
					if a == b {
						return 0
					}
					return 1
				})
				var snapshot strings.Builder
				for _, pos := range positions {
					snapshot.WriteString(partials[pos].String())
				}
				onUpdate(StreamContent{Content: snapshot.String(), Phase: phase})
			}
		}
		if evt.Type == "response.failed" {
			if status, ok := codexResponseErrorStatus(string(evt.Response.Error.Code)); ok {
				failure := &protocoltypes.HTTPError{StatusCode: status, Code: string(evt.Response.Error.Code), Wrapped: fmt.Errorf("Codex response failed (%s)", evt.Response.Error.Code)}
				if transportResponse != nil {
					failure.RetryAfter = common.RetryAfter(transportResponse.Header, time.Now())
				}
				terminalError = failure
			}
		}
		if evt.Type == "response.completed" || evt.Type == "response.failed" || evt.Type == "response.incomplete" {
			evtResp := evt.Response
			if evtResp.ID != "" {
				evtRespCopy := evtResp
				resp = &evtRespCopy
			}
			break // A terminal event completes this model operation even if the transport stays open.
		}
	}
	if terminalError != nil {
		return nil, terminalError
	}
	err := stream.Err()
	if err != nil {
		fields := map[string]any{
			"requested_model":    model,
			"resolved_model":     resolvedModel,
			"messages_count":     len(messages),
			"tools_count":        len(tools),
			"account_id_present": accountID != "",
			"error":              "provider request failed",
		}
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			fields["status_code"] = apiErr.StatusCode
			fields["api_type"] = apiErr.Type
			if _, known := codexResponseErrorStatus(apiErr.Code); known || !p.registered {
				fields["api_code"] = apiErr.Code
			}
			if !p.registered {
				fields["api_param"] = apiErr.Param
			}
			fields["api_message"] = "provider request failed"
			if apiErr.StatusCode == 400 {
				fields["hint"] = "verify account id header and model compatibility for codex backend"
			}
			if apiErr.Response != nil {
				fields["request_id"] = apiErr.Response.Header.Get("X-Request-ID")
			}
		}
		logger.ErrorCF("provider.codex", "Codex API call failed", fields)
		if p.registered && apiErr != nil {
			code := ""
			if _, ok := codexResponseErrorStatus(apiErr.Code); ok {
				code = apiErr.Code
			}
			failure := &protocoltypes.HTTPError{StatusCode: apiErr.StatusCode, Code: code, Wrapped: fmt.Errorf("ChatGPT model request failed (HTTP %d)", apiErr.StatusCode)}
			if apiErr.Response != nil {
				failure.RetryAfter = common.RetryAfter(apiErr.Response.Header, time.Now())
			}
			return nil, failure
		}
		return nil, fmt.Errorf("codex API call: %w", err)
	}
	if resp == nil || resp.Status != "completed" {
		fields := map[string]any{
			"requested_model":    model,
			"resolved_model":     resolvedModel,
			"messages_count":     len(messages),
			"tools_count":        len(tools),
			"account_id_present": accountID != "",
		}
		logger.ErrorCF("provider.codex", "Codex stream ended without completed response event", fields)
		return nil, fmt.Errorf("codex API call: stream ended without completed response: %w", io.ErrUnexpectedEOF)
	}

	return parseCodexResponse(resp), nil
}

// SupportsImageInput remains false until the Responses adapter serializes Media.
func (p *CodexProvider) SupportsImageInput() bool { return false }

func (p *CodexProvider) GetDefaultModel() string {
	return codexDefaultModel
}

func (p *CodexProvider) SupportsNativeSearch() bool {
	return p.enableWebSearch
}

func resolveCodexModel(model string) (string, string) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return codexDefaultModel, "empty model"
	}

	if after, ok := strings.CutPrefix(m, "openai/"); ok {
		m = after
	} else if strings.Contains(m, "/") {
		return codexDefaultModel, "non-openai model namespace"
	}

	unsupportedPrefixes := []string{
		"glm",
		"claude",
		"anthropic",
		"gemini",
		"google",
		"moonshot",
		"kimi",
		"qwen",
		"deepseek",
		"llama",
		"meta-llama",
		"mistral",
		"grok",
		"xai",
		"zhipu",
	}
	for _, prefix := range unsupportedPrefixes {
		if strings.HasPrefix(m, prefix) {
			return codexDefaultModel, "unsupported model prefix"
		}
	}

	if strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") {
		return m, ""
	}

	return codexDefaultModel, "unsupported model family"
}

func buildCodexParams(
	messages []Message, tools []ToolDefinition, model string, options map[string]any, enableWebSearch bool,
) responses.ResponseNewParams {
	var inputItems responses.ResponseInputParam
	var instructions string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Use the full concatenated system prompt (static + dynamic + summary)
			// as instructions. This keeps behavior consistent with Anthropic and
			// OpenAI-compat adapters where the complete system context lives in
			// one place. Prefix caching is handled by prompt_cache_key below,
			// not by splitting content across instructions vs input messages.
			instructions = msg.Content
		case "user":
			if msg.ToolCallID != "" {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
						CallID: msg.ToolCallID,
						Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
							OfString: openai.Opt(msg.Content),
						},
					},
				})
			} else {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleUser,
						Content: responses.EasyInputMessageContentUnionParam{OfString: openai.Opt(msg.Content)},
					},
				})
			}
		case "assistant":
			inputStart := len(inputItems)
			if len(msg.ToolCalls) > 0 {
				if msg.Content != "" {
					inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
						OfMessage: &responses.EasyInputMessageParam{
							Role:    responses.EasyInputMessageRoleAssistant,
							Content: responses.EasyInputMessageContentUnionParam{OfString: openai.Opt(msg.Content)},
						},
					})
				}
				for _, tc := range msg.ToolCalls {
					name, args, ok := resolveCodexToolCall(tc)
					if !ok {
						logger.WarnCF("provider.codex", "Skipping invalid tool call in history", map[string]any{
							"call_id": tc.ID,
						})
						continue
					}
					inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
						OfFunctionCall: &responses.ResponseFunctionToolCallParam{
							CallID:    tc.ID,
							Name:      name,
							Arguments: args,
						},
					})
				}
			} else {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{OfString: openai.Opt(msg.Content)},
					},
				})
			}
			if msg.ContentPhase != "" {
				for j := inputStart; j < len(inputItems); j++ {
					if inputItems[j].OfMessage != nil {
						inputItems[j].OfMessage.SetExtraFields(map[string]any{"phase": string(msg.ContentPhase)})
					}
				}
			}
		case "tool":
			inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: msg.ToolCallID,
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.Opt(msg.Content),
					},
				},
			})
		}
	}

	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Instructions: openai.Opt(instructions),
		Store:        openai.Opt(false),
	}

	if instructions != "" {
		params.Instructions = openai.Opt(instructions)
	} else {
		// ChatGPT Codex backend requires instructions to be present.
		params.Instructions = openai.Opt(defaultCodexInstructions)
	}

	// Prompt caching: pass a stable cache key so OpenAI can bucket requests
	// and reuse prefix KV cache across calls with the same key.
	// See: https://platform.openai.com/docs/guides/prompt-caching
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		params.PromptCacheKey = openai.Opt(cacheKey)
	}

	if len(tools) > 0 || enableWebSearch {
		params.Tools = translateToolsForCodex(tools, enableWebSearch)
	}

	return params
}

func resolveCodexToolCall(tc ToolCall) (name string, arguments string, ok bool) {
	name = tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}
	if name == "" {
		return "", "", false
	}

	if len(tc.Arguments) > 0 {
		argsJSON, err := json.Marshal(tc.Arguments)
		if err != nil {
			return "", "", false
		}
		return name, string(argsJSON), true
	}

	if tc.Function != nil && tc.Function.Arguments != "" {
		return name, tc.Function.Arguments, true
	}

	return name, "{}", true
}

func translateToolsForCodex(tools []ToolDefinition, enableWebSearch bool) []responses.ToolUnionParam {
	capHint := len(tools)
	if enableWebSearch {
		capHint++
	}
	result := make([]responses.ToolUnionParam, 0, capHint)
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		if enableWebSearch && strings.EqualFold(t.Function.Name, "web_search") {
			continue
		}
		ft := responses.FunctionToolParam{
			Name:       t.Function.Name,
			Parameters: t.Function.Parameters,
			Strict:     openai.Opt(false),
		}
		if t.Function.Description != "" {
			ft.Description = openai.Opt(t.Function.Description)
		}
		result = append(result, responses.ToolUnionParam{OfFunction: &ft})
	}
	if enableWebSearch {
		result = append(result, responses.ToolParamOfWebSearch(responses.WebSearchToolTypeWebSearch))
	}
	return result
}

func parseCodexResponse(resp *responses.Response) *LLMResponse {
	var content strings.Builder
	var toolCalls []ToolCall
	var contentPhase ContentPhase

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			phase := codexMessagePhase(item)
			if phase != ContentPhaseUnknown && phase != ContentPhaseAnswer {
				continue
			}
			if phase != "" {
				contentPhase = phase
			}
			for _, c := range item.Content {
				if c.Type == "output_text" {
					content.WriteString(c.Text)
				}
			}
		case "function_call":
			var args map[string]any
			if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
				args = map[string]any{"raw": item.Arguments}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	if resp.Status == "incomplete" {
		finishReason = "length"
	}

	var usage *UsageInfo
	if resp.Usage.TotalTokens > 0 {
		usage = &UsageInfo{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
			TotalTokens:      int(resp.Usage.TotalTokens),
		}
	}

	return &LLMResponse{
		Content:      content.String(),
		ContentPhase: contentPhase,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usage,
	}
}

func createCodexTokenSource() func() (string, string, error) {
	return createCodexAccountTokenSource(config.LegacyOpenAIAccountID)
}

func createCodexAccountTokenSource(accountID string) func() (string, string, error) {
	return func() (string, string, error) {
		cred, err := auth.AccountCredential(accountID)
		if err != nil {
			return "", "", fmt.Errorf("loading Codex account credentials: %w", err)
		}
		return cred.AccessToken, cred.AccountID, nil
	}
}

// The pinned SDK preserves newer message phase fields as JSON extras.
func codexMessagePhase(item responses.ResponseOutputItemUnion) ContentPhase {
	var value struct {
		Phase ContentPhase `json:"phase"`
	}
	_ = json.Unmarshal([]byte(item.RawJSON()), &value)
	return value.Phase
}

// Codes are a fixed provider contract; free-form error messages never enter diagnostics.
func codexResponseErrorStatus(code string) (int, bool) {
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "rate_limit_exceeded":
		return 429, true
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return 503, true
	case "subscription_sharing_invalid_user":
		return 401, true
	case "subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		return 403, true
	case "subscription_sharing_unsupported_capability", "invalid_prompt", "invalid_request":
		return 400, true
	case "server_error":
		return 500, true
	}
	return 0, false
}
