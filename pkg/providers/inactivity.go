package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

const DefaultInactivityTimeout = config.DefaultModelInactivityTimeout

type inactivityDeliveryKey struct{}

// WithoutModelInactivity excludes synchronous preview delivery from model wait
// time. Only decoded provider events reset the underlying progress clock.
func WithoutModelInactivity(ctx context.Context, delivery func()) {
	if pause, ok := ctx.Value(inactivityDeliveryKey{}).(func() func()); ok {
		resume := pause()
		defer resume()
	}
	delivery()
}

// InactivityTimeoutError ends only the stalled attempt, leaving its request live.
type InactivityTimeoutError struct{ Timeout time.Duration }

func (e *InactivityTimeoutError) Error() string {
	return fmt.Sprintf("model inactivity timeout after %s", e.Timeout)
}
func (*InactivityTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// RunModelAttempt bounds one synchronous provider call. Callers keep local tool
// execution, retry waits, and delivery outside this scope. Providers must honor ctx.
func RunModelAttempt(
	ctx context.Context,
	limit time.Duration,
	call func(context.Context) (*LLMResponse, error),
) (*LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultInactivityTimeout
	}
	failure := &InactivityTimeoutError{Timeout: limit}
	attempt, cancel := context.WithCancelCause(ctx)
	var mu sync.Mutex
	lastProgress := time.Now()
	var pausedAt time.Time
	pauses := 0
	wake := make(chan struct{}, 1)
	attempt = protocoltypes.WithModelProgress(attempt, func() {
		mu.Lock()
		if attempt.Err() == nil {
			lastProgress = time.Now()
			if pauses > 0 {
				pausedAt = lastProgress
			}
		}
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	attempt = context.WithValue(attempt, inactivityDeliveryKey{}, func() func() {
		mu.Lock()
		if pauses == 0 {
			pausedAt = time.Now()
		}
		pauses++
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
		return func() {
			mu.Lock()
			pauses--
			if pauses == 0 {
				lastProgress = lastProgress.Add(time.Since(pausedAt))
			}
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	})
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTimer(limit)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-attempt.Done():
				return
			case <-wake:
			case <-timer.C:
			}
			mu.Lock()
			remaining := limit - time.Since(lastProgress)
			if pauses > 0 {
				remaining = limit
			}
			if remaining <= 0 {
				cancel(failure)
				mu.Unlock()
				return
			}
			timer.Reset(remaining)
			mu.Unlock()
		}
	}()
	defer func() {
		close(done)
		<-stopped
		cancel(nil)
	}()
	response, err := call(attempt)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if context.Cause(attempt) == failure {
		return nil, failure
	}
	return response, err
}
