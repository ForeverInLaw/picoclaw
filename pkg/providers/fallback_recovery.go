package providers

import (
	"context"
	"errors"
	"time"
)

const maxServiceUnavailableWait = 30 * time.Second

// CandidateUnavailableError excludes an administratively unavailable or
// unauthorized connection. A known RetryAt permits a later attempt without
// treating a quota pause as an administrator's disabled state.
type CandidateUnavailableError struct {
	Reason  FailoverReason
	RetryAt time.Time
	Message string
}

func (e *CandidateUnavailableError) Error() string { return e.Message }

// ExecuteRecoveryCandidate first tries every available configured candidate.
// Only an outstanding HTTP 503 starts indefinite recovery; eligible candidates
// are then revisited in configured order until success or cancellation. onWait
// updates the caller's single waiting status, outside answer/tool content.
func (fc *FallbackChain) ExecuteRecoveryCandidate(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(context.Context, FallbackCandidate) (*LLMResponse, error),
	onWait func(delay time.Duration, failure error),
) (*FallbackResult, error) {
	type recoveryState struct {
		next     time.Time
		failure  *FailoverError
		excluded bool
		unavail  *CandidateUnavailableError
		retries  int
	}
	states := make(map[string]*recoveryState, len(candidates))
	for _, candidate := range candidates {
		state := &recoveryState{}
		if !fc.cooldown.IsAvailable(candidate.StableKey()) {
			state.failure = fc.cooldown.Failure(candidate.StableKey())
		}
		states[candidate.StableKey()] = state
	}
	trackedRun := func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error) {
		state := states[candidate.StableKey()]
		response, err := run(ctx, candidate)
		state.failure = ClassifyCandidateError(err, candidate)
		state.unavail = nil
		if errors.As(err, &state.unavail) {
			state.excluded = state.unavail.RetryAt.IsZero()
			state.next = state.unavail.RetryAt
			return response, err
		}
		if state.failure != nil && state.failure.Status == 503 {
			delay := min(time.Second<<min(state.retries, 5), maxServiceUnavailableWait)
			state.retries++
			state.failure.RetryAfter = max(delay, state.failure.RetryAfter)
			return response, state.failure
		}
		return response, err
	}

	eligible := candidates
	recovery := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := fc.execute(ctx, eligible, trackedRun, recovery)
		if err == nil {
			return result, nil
		}
		var exhausted *FallbackExhaustedError
		if !errors.As(err, &exhausted) {
			return nil, err
		}
		for _, attempt := range exhausted.Attempts {
			state := states[attempt.Candidate.StableKey()]
			var unavailable *CandidateUnavailableError
			if errors.As(attempt.Error, &unavailable) {
				state.unavail = unavailable
				state.excluded = unavailable.RetryAt.IsZero()
				state.next = unavailable.RetryAt
			}
			if state.unavail != nil {
				continue
			}
			if state.failure != nil {
				state.excluded = !state.failure.IsTransient() &&
					!(state.failure.Reason == FailoverBilling && state.failure.RetryAfter > 0)
			}
			state.next = time.Now().Add(fc.cooldown.CooldownRemaining(attempt.Candidate.StableKey()))
		}
		var outstanding503 *FailoverError
		var next time.Time
		eligible = nil
		now := time.Now()
		for _, candidate := range candidates {
			state := states[candidate.StableKey()]
			if state.excluded {
				continue
			}
			if state.failure != nil && state.failure.Status == 503 {
				outstanding503 = state.failure
			}
			if !now.Before(state.next) {
				eligible = append(eligible, candidate)
			}
			if next.IsZero() || state.next.Before(next) {
				next = state.next
			}
		}
		if outstanding503 == nil {
			return nil, err
		}
		recovery = true
		if len(eligible) > 0 {
			continue
		}
		delay := max(time.Duration(0), next.Sub(now))
		reportRouting(
			ctx,
			RoutingEvent{
				Phase:          "waiting",
				HTTPStatus:     503,
				Reason:         "service_unavailable",
				NextEligibleAt: time.Now().UTC().Add(delay),
			},
		)
		if onWait != nil {
			onWait(delay, outstanding503)
		}
		// Retry-After controls outbound calls, but live account removal must
		// terminate the wait without waiting for that potentially long pause.
		for {
			poll := next.Sub(time.Now())
			for _, candidate := range candidates {
				if candidate.AccountID != "" {
					poll = min(poll, time.Second)
					break
				}
			}
			timer := time.NewTimer(max(time.Duration(0), poll))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			recoverable := false
			for _, candidate := range candidates {
				state := states[candidate.StableKey()]
				if state.excluded {
					continue
				}
				availability, availabilityErr := fc.cooldown.AccountAvailabilityContext(ctx, candidate.AccountID)
				if availabilityErr != nil {
					return nil, availabilityErr
				}
				if !availability.Available && availability.NextEligibleAt.IsZero() {
					state.excluded = true
					reportRouting(ctx, RoutingEvent{
						Phase: "skipped", Candidate: candidate.RoutingIdentity(), Reason: string(availability.Reason),
					})
					continue
				}
				recoverable = recoverable || state.failure != nil && state.failure.Status == 503
			}
			if !recoverable {
				return nil, err
			}
			if !time.Now().Before(next) {
				break
			}
		}
		for _, candidate := range candidates {
			state := states[candidate.StableKey()]
			if !state.excluded && !time.Now().Before(state.next) {
				eligible = append(eligible, candidate)
			}
		}
	}
}
