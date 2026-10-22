package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// Capture failures before OIDC's diagnostic wrapping discards transport causes.
// Each verification owns its boundary, including background JWKS fetches.
type oidcHTTPBoundary struct {
	ctx       context.Context
	transport http.RoundTripper
	mu        sync.Mutex
	failure   error
}

func (b *oidcHTTPBoundary) record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failure = err
}

func (b *oidcHTTPBoundary) RoundTrip(request *http.Request) (*http.Response, error) {
	// coreos deliberately detaches JWKS fetch cancellation; this verification's
	// private client restores it so a canceled account request cannot leak work.
	response, err := b.transport.RoundTrip(request.WithContext(b.ctx))
	if err != nil {
		b.record(&TokenRequestError{Wrapped: err})
		return nil, err
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		b.record(
			&TokenRequestError{
				StatusCode: response.StatusCode,
				RetryAfter: common.RetryAfter(response.Header, time.Now()),
			},
		)
	}
	response.Body = &oidcHTTPBody{ReadCloser: response.Body, boundary: b}
	return response, nil
}

func (b *oidcHTTPBoundary) verificationError(message string) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	return errors.New(message)
}

type oidcHTTPBody struct {
	io.ReadCloser
	boundary *oidcHTTPBoundary
}

func (body *oidcHTTPBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		body.boundary.record(&TokenRequestError{Wrapped: err})
	}
	return count, err
}
