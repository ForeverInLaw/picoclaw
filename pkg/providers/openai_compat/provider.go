package openai_compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	ExtraContent           = protocoltypes.ExtraContent
	GoogleExtra            = protocoltypes.GoogleExtra
	ReasoningDetail        = protocoltypes.ReasoningDetail
)

type Provider struct {
	apiKey         string
	apiBase        string
	maxTokensField string // Field name for max tokens (e.g., "max_completion_tokens" for o1/glm models)
	httpClient     *http.Client
}

type Option func(*Provider)

const defaultRequestTimeout = common.DefaultRequestTimeout

func WithMaxTokensField(maxTokensField string) Option {
	return func(p *Provider) {
		p.maxTokensField = maxTokensField
	}
}

func WithRequestTimeout(timeout time.Duration) Option {
	return func(p *Provider) {
		if timeout > 0 {
			p.httpClient.Timeout = timeout
		}
	}
}

func NewProvider(apiKey, apiBase, proxy string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:     apiKey,
		apiBase:    strings.TrimRight(apiBase, "/"),
		httpClient: common.NewHTTPClient(proxy),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	return p
}

func NewProviderWithMaxTokensField(apiKey, apiBase, proxy, maxTokensField string) *Provider {
	return NewProvider(apiKey, apiBase, proxy, WithMaxTokensField(maxTokensField))
}

func NewProviderWithMaxTokensFieldAndTimeout(
	apiKey, apiBase, proxy, maxTokensField string,
	requestTimeoutSeconds int,
) *Provider {
	return NewProvider(
		apiKey,
		apiBase,
		proxy,
		WithMaxTokensField(maxTokensField),
		WithRequestTimeout(time.Duration(requestTimeoutSeconds)*time.Second),
	)
}

func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	if p.apiBase == "" {
		return nil, fmt.Errorf("API base not configured")
	}

	_, requestBody := p.buildRequestBody(messages, tools, model, options)
	resp, err := p.doChatRequest(ctx, requestBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, common.HandleErrorResponse(resp, p.apiBase)
	}

	return common.ReadAndParseResponse(resp, p.apiBase)
}

func (p *Provider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(content string),
) (*LLMResponse, error) {
	if p.apiBase == "" {
		return nil, fmt.Errorf("API base not configured")
	}

	_, requestBody := p.buildRequestBody(messages, tools, model, options)
	requestBody["stream"] = true

	resp, err := p.doChatRequest(ctx, requestBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, common.HandleErrorResponse(resp, p.apiBase)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/event-stream") {
		return common.ReadAndParseResponse(resp, p.apiBase)
	}

	return parseStreamingResponse(resp.Body, onUpdate)
}

func normalizeModel(model, apiBase string) string {
	before, after, ok := strings.Cut(model, "/")
	if !ok {
		return model
	}

	if strings.Contains(strings.ToLower(apiBase), "openrouter.ai") {
		return model
	}

	prefix := strings.ToLower(before)
	switch prefix {
	case "litellm", "moonshot", "nvidia", "groq", "ollama", "deepseek", "google",
		"openrouter", "zhipu", "mistral", "vivgrid", "minimax":
		return after
	default:
		return model
	}
}

// supportsPromptCacheKey reports whether the given API base is known to
// support the prompt_cache_key request field. Currently only OpenAI's own
// API and Azure OpenAI support this. All other OpenAI-compatible providers
// (Mistral, Gemini, DeepSeek, Groq, etc.) reject unknown fields with 422 errors.
func supportsPromptCacheKey(apiBase string) bool {
	u, err := url.Parse(apiBase)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com")
}

func (p *Provider) buildRequestBody(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (string, map[string]any) {
	model = normalizeModel(model, p.apiBase)

	requestBody := map[string]any{
		"model":    model,
		"messages": common.SerializeMessages(messages),
	}

	if len(tools) > 0 {
		requestBody["tools"] = tools
		requestBody["tool_choice"] = "auto"
	}

	if maxTokens, ok := common.AsInt(options["max_tokens"]); ok {
		fieldName := p.maxTokensField
		if fieldName == "" {
			lowerModel := strings.ToLower(model)
			if strings.Contains(lowerModel, "glm") || strings.Contains(lowerModel, "o1") ||
				strings.Contains(lowerModel, "gpt-5") {
				fieldName = "max_completion_tokens"
			} else {
				fieldName = "max_tokens"
			}
		}
		requestBody[fieldName] = maxTokens
	}

	if temperature, ok := common.AsFloat(options["temperature"]); ok {
		lowerModel := strings.ToLower(model)
		if strings.Contains(lowerModel, "kimi") && strings.Contains(lowerModel, "k2") {
			requestBody["temperature"] = 1.0
		} else {
			requestBody["temperature"] = temperature
		}
	}

	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		if supportsPromptCacheKey(p.apiBase) {
			requestBody["prompt_cache_key"] = cacheKey
		}
	}

	return model, requestBody
}

func (p *Provider) doChatRequest(ctx context.Context, requestBody map[string]any) (*http.Response, error) {
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.apiBase+"/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	return resp, nil
}

type streamedToolCall struct {
	ID               string
	Type             string
	Name             string
	Arguments        string
	ThoughtSignature string
}

func parseStreamingResponse(body io.Reader, onUpdate func(content string)) (*LLMResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	var content strings.Builder
	var reasoningContent strings.Builder
	var reasoning strings.Builder
	var reasoningDetails []ReasoningDetail
	var toolCalls []streamedToolCall
	var finishReason string
	var usage *UsageInfo

	processEvent := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "[DONE]" {
			return nil
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string            `json:"content"`
					ReasoningContent string            `json:"reasoning_content"`
					Reasoning        string            `json:"reasoning"`
					ReasoningDetails []ReasoningDetail `json:"reasoning_details"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function *struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
						ExtraContent *struct {
							Google *struct {
								ThoughtSignature string `json:"thought_signature"`
							} `json:"google"`
						} `json:"extra_content"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *UsageInfo `json:"usage"`
		}

		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return fmt.Errorf("failed to decode streaming chunk: %w", err)
		}

		if chunk.Usage != nil {
			usage = chunk.Usage
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if onUpdate != nil {
					onUpdate(content.String())
				}
			}
			if choice.Delta.ReasoningContent != "" {
				reasoningContent.WriteString(choice.Delta.ReasoningContent)
			}
			if choice.Delta.Reasoning != "" {
				reasoning.WriteString(choice.Delta.Reasoning)
			}
			if len(choice.Delta.ReasoningDetails) > 0 {
				reasoningDetails = append(reasoningDetails, choice.Delta.ReasoningDetails...)
			}
			for _, tc := range choice.Delta.ToolCalls {
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, streamedToolCall{})
				}
				current := &toolCalls[tc.Index]
				if tc.ID != "" {
					current.ID = tc.ID
				}
				if tc.Type != "" {
					current.Type = tc.Type
				}
				if tc.Function != nil {
					if tc.Function.Name != "" {
						current.Name = tc.Function.Name
					}
					current.Arguments += tc.Function.Arguments
				}
				if tc.ExtraContent != nil && tc.ExtraContent.Google != nil &&
					tc.ExtraContent.Google.ThoughtSignature != "" {
					current.ThoughtSignature = tc.ExtraContent.Google.ThoughtSignature
				}
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}

		return nil
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if len(dataLines) > 0 {
				if err := processEvent(strings.Join(dataLines, "\n")); err != nil {
					return nil, err
				}
				dataLines = dataLines[:0]
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed reading streaming response: %w", err)
	}
	if len(dataLines) > 0 {
		if err := processEvent(strings.Join(dataLines, "\n")); err != nil {
			return nil, err
		}
	}

	normalizedToolCalls := make([]ToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.ID == "" && tc.Name == "" && tc.Arguments == "" {
			continue
		}
		arguments := make(map[string]any)
		if strings.TrimSpace(tc.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &arguments); err != nil {
				arguments["raw"] = tc.Arguments
			}
		}
		toolCall := ToolCall{
			ID:        tc.ID,
			Type:      tc.Type,
			Name:      tc.Name,
			Arguments: arguments,
		}
		if tc.ThoughtSignature != "" {
			toolCall.ExtraContent = &ExtraContent{
				Google: &GoogleExtra{
					ThoughtSignature: tc.ThoughtSignature,
				},
			}
		}
		normalizedToolCalls = append(normalizedToolCalls, toolCall)
	}

	if finishReason == "" {
		finishReason = "stop"
	}

	return &LLMResponse{
		Content:          content.String(),
		ReasoningContent: reasoningContent.String(),
		Reasoning:        reasoning.String(),
		ReasoningDetails: reasoningDetails,
		ToolCalls:        normalizedToolCalls,
		FinishReason:     finishReason,
		Usage:            usage,
	}, nil
}
