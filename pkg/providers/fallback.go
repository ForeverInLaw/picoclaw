package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FallbackChain orchestrates model fallback across multiple candidates.
type FallbackChain struct {
	cooldown *CooldownTracker
}

// FallbackCandidate represents one model/provider to try.
type FallbackCandidate struct {
	Provider string
	Model    string
	// ConfigName retains the configured alias and its endpoint/credential selection.
	ConfigName string
	ConfigKey  string
	ConfigID   string
	AccountID  string
}

// StableKey keeps same-model configurations separate in caches and cooldowns.
func (c FallbackCandidate) StableKey() string {
	if c.ConfigKey != "" {
		return c.ConfigKey
	}
	if c.ConfigName != "" {
		return "model_name:" + c.ConfigName
	}
	return ModelKey(c.Provider, c.Model)
}

// FallbackResult contains the successful response and metadata about all attempts.
type FallbackResult struct {
	Response  *LLMResponse
	Provider  string
	Model     string
	Attempts  []FallbackAttempt
	Candidate FallbackCandidate
}

// FallbackAttempt records one attempt in the fallback chain.
type FallbackAttempt struct {
	Provider  string
	Model     string
	Error     error
	Reason    FailoverReason
	Duration  time.Duration
	Skipped   bool // true if cooldown or candidate eligibility prevented an outbound request
	Candidate FallbackCandidate
}

// NewFallbackChain creates a new fallback chain with the given cooldown tracker.
func NewFallbackChain(cooldown *CooldownTracker) *FallbackChain {
	return &FallbackChain{cooldown: cooldown}
}

// ResolveCandidates parses model config into a deduplicated candidate list.
func ResolveCandidates(cfg ModelConfig, defaultProvider string) []FallbackCandidate {
	return ResolveCandidatesWithLookup(cfg, defaultProvider, nil)
}

func ResolveCandidatesWithLookup(
	cfg ModelConfig,
	defaultProvider string,
	lookup func(raw string) (resolved string, ok bool),
) []FallbackCandidate {
	seen := make(map[string]bool)
	var candidates []FallbackCandidate

	addCandidate := func(raw string) {
		candidateRaw := strings.TrimSpace(raw)
		configName := ""
		if lookup != nil {
			if resolved, ok := lookup(candidateRaw); ok {
				configName = candidateRaw
				candidateRaw = resolved
			}
		}

		ref := ParseModelRef(candidateRaw, defaultProvider)
		if ref == nil {
			return
		}
		key := ModelKey(ref.Provider, ref.Model)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, FallbackCandidate{
			Provider:   ref.Provider,
			Model:      ref.Model,
			ConfigName: configName,
		})
	}

	// Primary first.
	addCandidate(cfg.Primary)

	// Then fallbacks.
	for _, fb := range cfg.Fallbacks {
		addCandidate(fb)
	}

	return candidates
}

// Execute runs the fallback chain for text/chat requests.
// It tries each candidate in order, respecting cooldowns and error classification.
//
// Behavior:
//   - Candidates in cooldown are skipped (logged as skipped attempt).
//   - context.Canceled aborts immediately (user abort, no fallback).
//   - Non-retriable errors (format) abort immediately.
//   - Retriable errors trigger fallback to next candidate.
//   - Success marks provider as good (resets cooldown).
//   - If all fail, returns aggregate error with all attempts.
func (fc *FallbackChain) Execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.ExecuteCandidate(ctx, candidates, func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
		return run(ctx, c.Provider, c.Model)
	})
}

func (fc *FallbackChain) ExecuteCandidate(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(context.Context, FallbackCandidate) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.execute(ctx, candidates, run, false)
}

// Retry continues the caller's failed operation after its backoff. A transient
// failure may retry its own candidate despite the cooldown it just established;
// unrelated requests still see that cooldown. The caller owns the attempt budget
// and Retry-After delay, and the retry's outcome updates global health as usual.
func (fc *FallbackChain) Retry(
	ctx context.Context,
	failure *FailoverError,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.RetryCandidate(ctx, failure, func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
		return run(ctx, c.Provider, c.Model)
	})
}

func (fc *FallbackChain) RetryCandidate(
	ctx context.Context,
	failure *FailoverError,
	run func(context.Context, FallbackCandidate) (*LLMResponse, error),
) (*FallbackResult, error) {
	if failure == nil || !failure.IsTransient() {
		return nil, fmt.Errorf("fallback: only transient failures can retry the current operation")
	}
	candidate := failure.Candidate
	if candidate.Provider == "" {
		candidate = FallbackCandidate{Provider: failure.Provider, Model: failure.Model}
	}
	return fc.execute(ctx, []FallbackCandidate{candidate}, run, true)
}

func (fc *FallbackChain) execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error),
	retryOwnFailure bool,
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		// Check context before each attempt.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		availability, availabilityErr := fc.cooldown.AccountAvailabilityContext(ctx, candidate.AccountID)
		if availabilityErr != nil {
			return nil, availabilityErr
		}
		if !availability.Available {
			reportRouting(
				ctx,
				RoutingEvent{
					Phase:          "skipped",
					Candidate:      candidate.RoutingIdentity(),
					Reason:         string(availability.Reason),
					NextEligibleAt: availability.NextEligibleAt,
				},
			)
			unavailable := &CandidateUnavailableError{
				Reason:  availability.Reason,
				RetryAt: availability.NextEligibleAt,
				Message: "account connection is unavailable",
			}
			result.Attempts = append(
				result.Attempts,
				FallbackAttempt{
					Candidate: candidate,
					Provider:  candidate.Provider,
					Model:     candidate.Model,
					Skipped:   true,
					Reason:    unavailable.Reason,
					Error:     unavailable,
				},
			)
			continue
		}

		// Each selected endpoint and credential has its own health state.
		cooldownKey := candidate.StableKey()
		if !retryOwnFailure && !fc.cooldown.IsAvailable(cooldownKey) {
			remaining := fc.cooldown.CooldownRemaining(cooldownKey)
			reportRouting(
				ctx,
				RoutingEvent{
					Phase:          "skipped",
					Candidate:      candidate.RoutingIdentity(),
					Reason:         "cooldown",
					NextEligibleAt: time.Now().UTC().Add(remaining),
				},
			)
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Skipped:   true,
				Reason:    FailoverRateLimit,
				Error: fmt.Errorf(
					"%s in cooldown (%s remaining)",
					cooldownKey,
					remaining.Round(time.Second),
				),
			})
			continue
		}

		// Execute the run function.
		start := time.Now()
		reportRouting(ctx, RoutingEvent{Phase: "attempt", Candidate: candidate.RoutingIdentity()})
		resp, err := run(ctx, candidate)
		elapsed := time.Since(start)
		routingOutcome(ctx, candidate, start, err)

		if err == nil {
			// Success.
			fc.cooldown.MarkSuccess(cooldownKey)
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			result.Candidate = candidate
			return result, nil
		}

		// Context cancellation: abort immediately, no fallback.
		if ctx.Err() != nil {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Error:     err,
				Duration:  elapsed,
			})
			return nil, ctx.Err()
		}

		var incompatible *IncompatibleCandidateError
		if errors.As(err, &incompatible) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate, Provider: candidate.Provider, Model: candidate.Model,
				Skipped: true, Reason: FailoverIncompatible, Error: err, Duration: elapsed,
			})
			continue
		}

		var unavailable *CandidateUnavailableError
		if errors.As(err, &unavailable) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate, Provider: candidate.Provider, Model: candidate.Model,
				Skipped: true, Reason: unavailable.Reason, Error: err, Duration: elapsed,
			})
			continue
		}

		// Classify the error.
		failErr := ClassifyCandidateError(err, candidate)

		if failErr == nil {
			// Unclassifiable error: do not fallback, return immediately.
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Error:     err,
				Duration:  elapsed,
			})
			return nil, fmt.Errorf("fallback: unclassified error from %s/%s: %w",
				candidate.Provider, candidate.Model, err)
		}

		// Non-retriable error: abort immediately.
		if !failErr.IsRetriable() {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Error:     failErr,
				Reason:    failErr.Reason,
				Duration:  elapsed,
			})
			return nil, failErr
		}

		// Retriable error: mark failure and continue to next candidate.
		if !fc.cooldown.recordAccountFailure(ctx, candidate, availability.SessionVersion, failErr) {
			fc.cooldown.RecordFailure(cooldownKey, failErr)
		}
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Candidate: candidate,
			Provider:  candidate.Provider,
			Model:     candidate.Model,
			Error:     failErr,
			Reason:    failErr.Reason,
			Duration:  elapsed,
		})

		// If this was the last candidate, return aggregate error.
		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	// All candidates were skipped (all in cooldown).
	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// ExecuteImage runs the fallback chain for image/vision requests.
// Simpler than Execute: no cooldown checks (image endpoints have different rate limits).
// Image dimension/size errors abort immediately (non-retriable).
func (fc *FallbackChain) ExecuteImage(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.ExecuteImageCandidate(
		ctx,
		candidates,
		func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
			return run(ctx, c.Provider, c.Model)
		},
	)
}

func (fc *FallbackChain) ExecuteImageCandidate(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(context.Context, FallbackCandidate) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("image fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		start := time.Now()
		reportRouting(ctx, RoutingEvent{Phase: "attempt", Candidate: candidate.RoutingIdentity()})
		resp, err := run(ctx, candidate)
		elapsed := time.Since(start)
		routingOutcome(ctx, candidate, start, err)

		if err == nil {
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			result.Candidate = candidate
			return result, nil
		}

		if ctx.Err() != nil {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Error:     err,
				Duration:  elapsed,
			})
			return nil, ctx.Err()
		}

		// Image dimension/size errors are non-retriable.
		errMsg := strings.ToLower(err.Error())
		if IsImageDimensionError(errMsg) || IsImageSizeError(errMsg) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Candidate: candidate,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Error:     err,
				Reason:    FailoverFormat,
				Duration:  elapsed,
			})
			return nil, &FailoverError{
				Reason:    FailoverFormat,
				Provider:  candidate.Provider,
				Model:     candidate.Model,
				Candidate: candidate,
				Wrapped:   err,
			}
		}

		// Any other error: record and try next.
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Candidate: candidate,
			Provider:  candidate.Provider,
			Model:     candidate.Model,
			Error:     err,
			Duration:  elapsed,
		})

		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// FallbackExhaustedError indicates all fallback candidates were tried and failed.
type FallbackExhaustedError struct {
	Attempts []FallbackAttempt
}

func (e *FallbackExhaustedError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fallback: all %d candidates failed:", len(e.Attempts)))
	for i, a := range e.Attempts {
		if a.Skipped {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: skipped (%s): %v", i+1, a.Provider, a.Model, a.Reason, a.Error))
		} else {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: %v (reason=%s, %s)",
				i+1, a.Provider, a.Model, a.Error, a.Reason, a.Duration.Round(time.Millisecond)))
		}
	}
	return sb.String()
}

// Unwrap exposes the final attempted request to shared recovery classification.
func (e *FallbackExhaustedError) Unwrap() error {
	for i := len(e.Attempts) - 1; i >= 0; i-- {
		if !e.Attempts[i].Skipped && e.Attempts[i].Error != nil {
			return e.Attempts[i].Error
		}
	}
	return nil
}
