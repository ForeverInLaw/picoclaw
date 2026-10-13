package protocoltypes

import "time"

// HTTPError retains transport metadata needed by a shared model retry policy.
type HTTPError struct {
	Code       string
	StatusCode int
	RetryAfter time.Duration
	Wrapped    error
}

func (e *HTTPError) Error() string { return e.Wrapped.Error() }
func (e *HTTPError) Unwrap() error { return e.Wrapped }
