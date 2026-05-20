package extract

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// ProviderLLM adapts a providers.LLMProvider to the ExtractionLLM
// interface, sending a system+user pair through Chat with a pinned model.
type ProviderLLM struct {
	Provider providers.LLMProvider
	Model    string
}

func (p *ProviderLLM) ExtractFacts(ctx context.Context, system, user string) (string, error) {
	if p == nil || p.Provider == nil {
		return "", fmt.Errorf("extract: no provider configured")
	}
	model := p.Model
	if model == "" {
		model = p.Provider.GetDefaultModel()
	}
	resp, err := p.Provider.Chat(ctx, []providers.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, nil, model, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("extract: nil response")
	}
	return resp.Content, nil
}
