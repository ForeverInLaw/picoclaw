package providers

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RoutingIdentity is the public identity of one configured choice, without keys or endpoints.
type RoutingIdentity struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	AccountID string `json:"account_id,omitempty"`
}

func (c FallbackCandidate) RoutingIdentity() RoutingIdentity {
	return RoutingIdentity{
		ID:        c.ConfigID,
		Name:      c.ConfigName,
		Provider:  c.Provider,
		Model:     c.Model,
		AccountID: c.AccountID,
	}
}

// RoutingEvent deliberately carries no raw error, response or request data.
type RoutingEvent struct {
	HTTPStatus          int             `json:"http_status,omitempty"`
	At                  time.Time       `json:"at"`
	Phase               string          `json:"phase"`
	Candidate           RoutingIdentity `json:"candidate"`
	Reason              string          `json:"reason,omitempty"`
	InactivityTimeoutMS int64           `json:"inactivity_timeout_ms,omitempty"`
	DurationMS          int64           `json:"duration_ms,omitempty"`
	NextEligibleAt      time.Time       `json:"next_eligible_at,omitzero"`
}

type routingObserverKey struct{}

func WithRoutingObserver(ctx context.Context, observer func(RoutingEvent)) context.Context {
	return context.WithValue(ctx, routingObserverKey{}, observer)
}

func reportRouting(ctx context.Context, event RoutingEvent) {
	if observer, ok := ctx.Value(routingObserverKey{}).(func(RoutingEvent)); ok && observer != nil {
		event.At = time.Now().UTC()
		observer(event)
	}
}

func routingOutcome(
	ctx context.Context,
	candidate FallbackCandidate,
	started time.Time,
	response *LLMResponse,
	err error,
) {
	event := RoutingEvent{
		Phase:      "success",
		Candidate:  candidate.RoutingIdentity(),
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err == nil && (response == nil || (strings.TrimSpace(response.Content) == "" && len(response.ToolCalls) == 0)) {
		event.Phase = "failed"
		event.Reason = "empty_answer"
	}
	if err != nil {
		event.Phase = "failed"
		event.Reason = "unknown"
		var inactivity *InactivityTimeoutError
		var unavailable *CandidateUnavailableError
		var incompatible *IncompatibleCandidateError
		switch {
		case errors.As(err, &inactivity):
			event.Reason = "inactivity_timeout"
			event.InactivityTimeoutMS = inactivity.Timeout.Milliseconds()
		case ctx.Err() != nil:
			event.Phase = "canceled"
			event.Reason = "canceled"
		case errors.As(err, &unavailable):
			event.Phase = "skipped"
			event.Reason = string(unavailable.Reason)
			event.NextEligibleAt = unavailable.RetryAt
		case errors.As(err, &incompatible):
			event.Phase = "skipped"
			event.Reason = string(FailoverIncompatible)
		default:
			if failure := ClassifyCandidateError(err, candidate); failure != nil {
				event.HTTPStatus = failure.Status
				event.Reason = string(failure.Reason)
				if failure.Status == 503 {
					event.Reason = "service_unavailable"
				}
			}
		}
	}
	reportRouting(ctx, event)
}

// RoutingAvailability uses the same health state as the executor.
type RoutingAvailability struct {
	HTTPStatus     int             `json:"http_status,omitempty"`
	Authorization  string          `json:"authorization,omitempty"`
	Candidate      RoutingIdentity `json:"candidate"`
	Available      bool            `json:"available"`
	Reason         string          `json:"reason,omitempty"`
	NextEligibleAt time.Time       `json:"next_eligible_at,omitzero"`
}

func (fc *FallbackChain) Availability(ctx context.Context, candidate FallbackCandidate) (RoutingAvailability, error) {
	result := RoutingAvailability{Candidate: candidate.RoutingIdentity(), Available: true}
	account, err := fc.cooldown.AccountAvailabilityContext(ctx, candidate.AccountID)
	if err != nil {
		return result, err
	}
	if !account.Available {
		result.Available = false
		result.Reason = string(account.Reason)
		result.NextEligibleAt = account.NextEligibleAt
		return result, nil
	}
	if !fc.cooldown.IsAvailable(candidate.StableKey()) {
		result.Available = false
		result.Reason = "cooldown"
		if failure := fc.cooldown.Failure(candidate.StableKey()); failure != nil {
			result.HTTPStatus = failure.Status
			result.Reason = string(failure.Reason)
			if failure.Status == 503 {
				result.Reason = "service_unavailable"
			}
		}
		if remaining := fc.cooldown.CooldownRemaining(candidate.StableKey()); remaining > 0 {
			result.NextEligibleAt = time.Now().UTC().Add(remaining)
		}
	}
	return result, nil
}
