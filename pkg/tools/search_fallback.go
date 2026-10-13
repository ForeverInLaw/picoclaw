package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

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
		parallel = &ParallelSearchProvider{keyPool: NewAPIKeyPool(opts.ParallelAPIKeys), baseURL: opts.ParallelBaseURL, client: client}
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
	if backup != nil {
		chosen = &fallbackSearchProvider{primary: chosen, backup: backup}
	}
	return chosen, min(count, 10), nil
}

// fallbackSearchProvider tries its backup once, only when a search can recover.
type fallbackSearchProvider struct{ primary, backup SearchProvider }

func (p *fallbackSearchProvider) ProviderName() string { return p.primary.ProviderName() }

func (p *fallbackSearchProvider) Search(ctx context.Context, query string, count int) ([]websource.SearchHit, error) {
	// Reserve time for at most two external requests, including a timed-out primary.
	ctx, cancel := context.WithTimeout(ctx, 2*searchTimeout)
	defer cancel()
	hits, err := p.primary.Search(ctx, query, count)
	if err == nil || ctx.Err() != nil || !isTransientSearchError(err) {
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
		return !status.quota && (status.status == http.StatusRequestTimeout || status.status == http.StatusTooManyRequests || status.status == http.StatusInternalServerError || status.status == http.StatusBadGateway || status.status == http.StatusServiceUnavailable || status.status == http.StatusGatewayTimeout)
	}
	// url.Error itself implements net.Error even for permanent TLS/protocol errors.
	var requestError *url.Error
	if errors.As(err, &requestError) {
		err = requestError.Err
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
