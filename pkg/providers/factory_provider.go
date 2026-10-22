// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	anthropicmessages "github.com/sipeed/picoclaw/pkg/providers/anthropic_messages"
	"github.com/sipeed/picoclaw/pkg/providers/azure"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// createClaudeAuthProvider creates a Claude provider using OAuth credentials from auth store.
func createClaudeAuthProvider() (LLMProvider, error) {
	cred, err := getCredential("anthropic")
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf("no credentials for anthropic. Run: picoclaw auth login --provider anthropic")
	}
	return NewClaudeProviderWithTokenSource(cred.AccessToken, createClaudeTokenSource()), nil
}

// createCodexAuthProvider creates a Codex provider using OAuth credentials from auth store.
func createCodexAuthProvider(accountIDs ...string) (LLMProvider, error) {
	accountID := config.LegacyOpenAIAccountID
	if len(accountIDs) > 0 && accountIDs[0] != "" {
		accountID = accountIDs[0]
	}
	account, err := auth.GetAccount(accountID)
	if err != nil {
		return nil, &CandidateUnavailableError{Reason: FailoverAuth, Message: "Codex account connection is unavailable"}
	}
	if account.Credential == nil {
		return nil, &CandidateUnavailableError{
			Reason:  FailoverAuth,
			Message: "Codex account connection requires authorization",
		}
	}
	if !account.Enabled {
		return nil, &CandidateUnavailableError{Reason: FailoverAuth, Message: "Codex account connection is disabled"}
	}
	cred := account.Credential
	provider := NewCodexProvider(cred.AccessToken, cred.AccountID)
	provider.contextTokenSource = func(ctx context.Context) (string, string, error) {
		state, err := auth.AccountStateContext(ctx, accountID)
		if err != nil || !state.Enabled || state.RequiresReauthorization {
			return "", "", &CandidateUnavailableError{
				Reason:  FailoverAuth,
				Message: "Codex account connection is unavailable",
			}
		}
		current, err := auth.AccountCredentialContext(ctx, accountID)
		if err != nil {
			var tokenError *auth.TokenRequestError
			if errors.As(err, &tokenError) {
				status := tokenError.StatusCode
				if tokenError.ReauthorizationRequired {
					status = 401
				}
				err = &protocoltypes.HTTPError{
					StatusCode: status,
					Code:       tokenError.Code,
					RetryAfter: tokenError.RetryAfter,
					Wrapped:    err,
				}
			} else {
				live, liveErr := auth.AccountStateContext(ctx, accountID)
				if ctx.Err() != nil {
					return "", "", ctx.Err()
				}
				if liveErr != nil || !live.Enabled || live.RequiresReauthorization {
					err = &CandidateUnavailableError{
						Reason:  FailoverAuth,
						Message: "Codex account connection is unavailable",
					}
				}
			}
			return "", "", err
		}
		return current.AccessToken, current.AccountID, nil
	}
	if account.Resource != "" {
		provider.registered = true
		// Registered ChatGPT accounts use the issued API resource; legacy CLI sessions retain their endpoint.
		client := openai.NewClient(
			option.WithBaseURL(account.Resource),
			option.WithAPIKey(cred.AccessToken),
			option.WithMaxRetries(0),
		)
		provider.client = &client
	}
	return provider, nil
}

// ExtractProtocol extracts the protocol prefix and model identifier from a model string.
// If no prefix is specified, it defaults to "openai".
// Examples:
//   - "openai/gpt-4o" -> ("openai", "gpt-4o")
//   - "anthropic/claude-sonnet-4.6" -> ("anthropic", "claude-sonnet-4.6")
//   - "gpt-4o" -> ("openai", "gpt-4o")  // default protocol
func ExtractProtocol(model string) (protocol, modelID string) {
	model = strings.TrimSpace(model)
	protocol, modelID, found := strings.Cut(model, "/")
	if !found {
		return "openai", model
	}
	return protocol, modelID
}

// CreateProviderFromConfig creates a provider based on the ModelConfig.
// It uses the protocol prefix in the Model field to determine which provider to create.
// Supported protocols: openai, litellm, novita, anthropic, anthropic-messages,
// antigravity, claude-cli, codex-cli, github-copilot
// Returns the provider, the model ID (without protocol prefix), and any error.
func CreateProviderFromConfig(cfg *config.ModelConfig) (LLMProvider, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config is nil")
	}

	if cfg.Model == "" {
		return nil, "", fmt.Errorf("model is required")
	}

	protocol, modelID := ExtractProtocol(cfg.Model)

	switch protocol {
	case "openai":
		// OpenAI with OAuth/token auth (Codex-style)
		if cfg.AuthMethod == "oauth" || cfg.AuthMethod == "token" {
			provider, err := createCodexAuthProvider(cfg.AccountID)
			if err != nil {
				return nil, "", err
			}
			return provider, modelID, nil
		}
		// OpenAI with API key
		if cfg.APIKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		return NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			cfg.APIKey,
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			cfg.RequestTimeout,
			cfg.ExtraBody,
		).withModelConfig(cfg), modelID, nil

	case "azure", "azure-openai":
		// Azure OpenAI uses deployment-based URLs, api-key header auth,
		// and always sends max_completion_tokens.
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for azure protocol")
		}
		if cfg.APIBase == "" {
			return nil, "", fmt.Errorf(
				"api_base is required for azure protocol (e.g., https://your-resource.openai.azure.com)",
			)
		}
		return azure.NewProviderWithTimeout(
			cfg.APIKey,
			cfg.APIBase,
			cfg.Proxy,
			cfg.RequestTimeout,
		), modelID, nil

	case "litellm", "openrouter", "groq", "zhipu", "gemini", "nvidia",
		"ollama", "moonshot", "shengsuanyun", "deepseek", "cerebras",
		"vivgrid", "volcengine", "vllm", "qwen", "qwen-intl", "qwen-international", "dashscope-intl",
		"qwen-us", "dashscope-us", "mistral", "avian", "minimax", "longcat", "modelscope", "novita",
		"coding-plan", "alibaba-coding", "qwen-coding":
		// All other OpenAI-compatible HTTP providers
		if cfg.APIKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		return NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			cfg.APIKey,
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			cfg.RequestTimeout,
			cfg.ExtraBody,
		).withModelConfig(cfg), modelID, nil

	case "anthropic":
		if cfg.AuthMethod == "oauth" || cfg.AuthMethod == "token" {
			// Use OAuth credentials from auth store
			provider, err := createClaudeAuthProvider()
			if err != nil {
				return nil, "", err
			}
			return provider, modelID, nil
		}
		// Use API key with HTTP API
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic protocol (model: %s)", cfg.Model)
		}
		return NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			cfg.APIKey,
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			cfg.RequestTimeout,
			cfg.ExtraBody,
		).withModelConfig(cfg), modelID, nil

	case "anthropic-messages":
		// Anthropic Messages API with native format (HTTP-based, no SDK)
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic-messages protocol (model: %s)", cfg.Model)
		}
		return anthropicmessages.NewProviderWithTimeout(
			cfg.APIKey,
			apiBase,
			cfg.RequestTimeout,
		), modelID, nil

	case "coding-plan-anthropic", "alibaba-coding-anthropic":
		// Alibaba Coding Plan with Anthropic-compatible API
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for %q protocol (model: %s)", protocol, cfg.Model)
		}
		return anthropicmessages.NewProviderWithTimeout(
			cfg.APIKey,
			apiBase,
			cfg.RequestTimeout,
		), modelID, nil

	case "antigravity":
		return NewAntigravityProvider(), modelID, nil

	case "claude-cli", "claudecli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		return NewClaudeCliProvider(workspace), modelID, nil

	case "codex-cli", "codexcli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		return NewCodexCliProvider(workspace), modelID, nil

	case "github-copilot", "copilot":
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "localhost:4321"
		}
		connectMode := cfg.ConnectMode
		if connectMode == "" {
			connectMode = "grpc"
		}
		provider, err := NewGitHubCopilotProvider(apiBase, connectMode, modelID)
		if err != nil {
			return nil, "", err
		}
		return provider, modelID, nil

	default:
		return nil, "", fmt.Errorf("unknown protocol %q in model %q", protocol, cfg.Model)
	}
}

// getDefaultAPIBase returns the default API base URL for a given protocol.
func getDefaultAPIBase(protocol string) string {
	switch protocol {
	case "openai":
		return "https://api.openai.com/v1"
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "litellm":
		return "http://localhost:4000/v1"
	case "novita":
		return "https://api.novita.ai/openai"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "zhipu":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "nvidia":
		return "https://integrate.api.nvidia.com/v1"
	case "ollama":
		return "http://localhost:11434/v1"
	case "moonshot":
		return "https://api.moonshot.cn/v1"
	case "shengsuanyun":
		return "https://router.shengsuanyun.com/api/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "cerebras":
		return "https://api.cerebras.ai/v1"
	case "vivgrid":
		return "https://api.vivgrid.com/v1"
	case "volcengine":
		return "https://ark.cn-beijing.volces.com/api/v3"
	case "qwen":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	case "qwen-intl", "qwen-international", "dashscope-intl":
		return "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
	case "qwen-us", "dashscope-us":
		return "https://dashscope-us.aliyuncs.com/compatible-mode/v1"
	case "coding-plan", "alibaba-coding", "qwen-coding":
		return "https://coding-intl.dashscope.aliyuncs.com/v1"
	case "coding-plan-anthropic", "alibaba-coding-anthropic":
		return "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic"
	case "vllm":
		return "http://localhost:8000/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "avian":
		return "https://api.avian.io/v1"
	case "minimax":
		return "https://api.minimaxi.com/v1"
	case "longcat":
		return "https://api.longcat.chat/openai"
	case "modelscope":
		return "https://api-inference.modelscope.cn/v1"
	default:
		return ""
	}
}

// DefaultAPIBaseForProtocol exposes the factory's endpoint defaults to
// configuration interfaces that compare credential destinations.
func DefaultAPIBaseForProtocol(protocol string) string {
	return getDefaultAPIBase(protocol)
}
