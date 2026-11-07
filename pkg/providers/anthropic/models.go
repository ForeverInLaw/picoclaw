package anthropicprovider

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ModelInfo is one model the provider's credential can use.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ListModels returns every model Anthropic's Models API lists for an API-key
// provider, newest first. Listing costs no credit, so it also validates a key:
// a revoked or wrong key fails with a 401 *anthropic.Error.
func (p *Provider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if p.requestTimeout > 0 {
		opts = append(opts, option.WithRequestTimeout(p.requestTimeout))
	}
	pager := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1000)}, opts...)
	models := []ModelInfo{}
	for pager.Next() {
		model := pager.Current()
		models = append(models, ModelInfo{ID: model.ID, DisplayName: model.DisplayName})
	}
	return models, pager.Err()
}
