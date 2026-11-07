package providers

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
	anthropicprovider "github.com/sipeed/picoclaw/pkg/providers/anthropic"
)

type ClaudeProvider struct {
	delegate  *anthropicprovider.Provider // single credential; nil when pool is set
	pool      *keyPool                    // Key pool model: the delegate follows the conversation
	configKey string
}

// ModelConfigKey lets agent resolution retain the exact factory selection.
func (p *ClaudeProvider) ModelConfigKey() string { return p.configKey }

func (p *ClaudeProvider) withModelConfig(mc *config.ModelConfig) *ClaudeProvider {
	p.configKey = ModelConfigKey(mc)
	return p
}

func NewClaudeProvider(token string) *ClaudeProvider {
	return &ClaudeProvider{
		delegate: anthropicprovider.NewProvider(token),
	}
}

func NewClaudeProviderWithBaseURL(token, apiBase string) *ClaudeProvider {
	return &ClaudeProvider{
		delegate: anthropicprovider.NewProviderWithBaseURL(token, apiBase),
	}
}

func NewClaudeProviderWithTokenSource(token string, tokenSource func() (string, error)) *ClaudeProvider {
	return &ClaudeProvider{
		delegate: anthropicprovider.NewProviderWithTokenSource(token, tokenSource),
	}
}

func NewClaudeProviderWithTokenSourceAndBaseURL(
	token string, tokenSource func() (string, error), apiBase string,
) *ClaudeProvider {
	return &ClaudeProvider{
		delegate: anthropicprovider.NewProviderWithTokenSourceAndBaseURL(token, tokenSource, apiBase),
	}
}

func newClaudeProviderWithDelegate(delegate *anthropicprovider.Provider) *ClaudeProvider {
	return &ClaudeProvider{delegate: delegate}
}

func newClaudeProviderWithKeyPool(pool *keyPool) *ClaudeProvider {
	return &ClaudeProvider{pool: pool}
}

// serve runs one request on the single delegate or on the pool key bound to
// the request's conversation.
func (p *ClaudeProvider) serve(
	ctx context.Context,
	options map[string]any,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (*LLMResponse, error) {
	if p.pool != nil {
		return p.pool.serve(ctx, options, call)
	}
	return call(p.delegate)
}

func (p *ClaudeProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	return p.serve(ctx, options, func(delegate *anthropicprovider.Provider) (*LLMResponse, error) {
		return delegate.Chat(ctx, messages, tools, model, options)
	})
}

func (p *ClaudeProvider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onUpdate func(content string),
) (*LLMResponse, error) {
	return p.serve(ctx, options, func(delegate *anthropicprovider.Provider) (*LLMResponse, error) {
		return delegate.ChatStream(ctx, messages, tools, model, options, onUpdate)
	})
}

// SupportsThinking implements ThinkingCapable, so the agent sends thinking_level;
// every Anthropic delegate, single or pool key, maps it.
func (p *ClaudeProvider) SupportsThinking() bool { return true }

func (p *ClaudeProvider) GetDefaultModel() string {
	if p.pool != nil {
		return p.pool.keys[0].client.GetDefaultModel()
	}
	return p.delegate.GetDefaultModel()
}

func createClaudeTokenSource() func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		cred, err := getCredential(ctx, "anthropic")
		if err != nil {
			return "", fmt.Errorf("loading auth credentials: %w", err)
		}
		if cred == nil {
			return "", fmt.Errorf("no credentials for anthropic. Run: picoclaw auth login --provider anthropic")
		}
		return cred.AccessToken, nil
	}
}
