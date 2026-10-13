package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/websource"
)

func newPreferredSearchProvider(opts WebSearchToolOptions) (SearchProvider, int, error) {
	parallelAvailable := opts.ParallelEnabled && len(opts.ParallelAPIKeys) > 0
	exaAvailable := opts.ExaEnabled && len(opts.ExaAPIKeys) > 0
	primary := strings.ToLower(strings.TrimSpace(opts.PrimaryProvider))
	switch primary {
	case "":
		if parallelAvailable {
			primary = "parallel"
		} else if exaAvailable {
			primary = "exa"
		} else {
			return nil, 0, nil
		}
	case "parallel", "exa":
	default:
		return nil, 0, fmt.Errorf("unknown primary search provider %q", opts.PrimaryProvider)
	}
	if primary == "parallel" && !parallelAvailable || primary == "exa" && !exaAvailable {
		return nil, 0, fmt.Errorf("%s is selected but enabled credentials are unavailable", primary)
	}
	client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
	if err != nil {
		return nil, 0, fmt.Errorf("create search HTTP client: %w", err)
	}
	var parallel, exa SearchProvider
	if parallelAvailable {
		parallel = &ParallelSearchProvider{
			keyPool: NewAPIKeyPool(opts.ParallelAPIKeys),
			baseURL: opts.ParallelBaseURL,
			client:  client,
		}
	}
	if exaAvailable {
		exa = &ExaSearchProvider{keyPool: NewAPIKeyPool(opts.ExaAPIKeys), baseURL: opts.ExaBaseURL, client: client}
	}
	chosen, backup, count := parallel, exa, opts.ParallelMaxResults
	if primary == "exa" {
		chosen, backup, count = exa, parallel, opts.ExaMaxResults
	}
	if count <= 0 {
		count = 5
	}
	chosen = &fallbackSearchProvider{primary: chosen, backup: backup}
	return chosen, min(count, 10), nil
}

// fallbackSearchProvider tries its backup once, only when a search can recover.
type fallbackSearchProvider struct{ primary, backup SearchProvider }

func (p *fallbackSearchProvider) ProviderName() string { return p.primary.ProviderName() }

func (p *fallbackSearchProvider) Search(ctx context.Context, query string, count int) ([]websource.SearchHit, error) {
	// Bound the complete operation, including credential repair and one backup.
	ctx, cancel := context.WithTimeout(ctx, 2*searchTimeout)
	defer cancel()
	hits, err := p.primary.Search(ctx, query, count)
	if err == nil || ctx.Err() != nil || p.backup == nil || !isTransientSearchError(err) {
		return hits, err
	}
	hits, backupErr := p.backup.Search(ctx, query, count)
	if backupErr != nil {
		return nil, fmt.Errorf("primary search failed (%v); backup search failed: %w", err, backupErr)
	}
	return hits, nil
}

func isTransientSearchError(err error) bool {
	var status *searchHTTPError
	if errors.As(err, &status) {
		if status.quota {
			return false
		}
		err = &protocoltypes.HTTPError{StatusCode: status.status, Wrapped: err}
	}
	failure := providers.ClassifyError(err, "", "")
	return failure != nil && failure.IsTransient()
}

// searchJSONWithKeys repairs credential failures with a finite pass over the
// configured key pool. It never repeats a key or hides provider/transport errors.
func searchJSONWithKeys(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	keys *APIKeyPool,
	provider string,
	payload, result any,
) error {
	var lastErr error
	iterator := keys.NewIterator()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, ok := iterator.Next()
		if !ok {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("%s credentials are unavailable", provider)
		}
		lastErr = searchJSON(ctx, client, endpoint, key, provider, payload, result)
		var failure *searchHTTPError
		if !errors.As(lastErr, &failure) ||
			!(failure.auth || failure.quota || failure.status == http.StatusTooManyRequests) {
			return lastErr
		}
	}
}
