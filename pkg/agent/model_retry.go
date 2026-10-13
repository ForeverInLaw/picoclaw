package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	modelMaxAttempts  = 3
	modelMaxRetryWait = 30 * time.Second
)

var errModelAttemptBudget = errors.New("model request attempt budget exhausted")

func modelRetryDelay(err error, retry int, waited time.Duration) (time.Duration, string, bool) {
	failure := providers.ClassifyError(err, "", "")
	if failure == nil || !failure.IsTransient() {
		return 0, "", false
	}
	delay := time.Second << retry
	if failure.RetryAfter > 0 {
		delay = failure.RetryAfter
	}
	// Never retry earlier than the API asked. An excessive delay ends recovery.
	if delay > modelMaxRetryWait-waited {
		return 0, string(failure.Reason), false
	}
	return delay, string(failure.Reason), true
}

func (al *AgentLoop) reportModelRetry(
	ctx context.Context,
	ts *turnState,
	attempt int,
	reason string,
	err error,
	delay time.Duration,
) {
	al.emitEvent(
		EventKindLLMRetry,
		ts.eventMeta("runTurn", "turn.llm.retry"),
		LLMRetryPayload{
			Attempt:    attempt,
			MaxRetries: modelMaxAttempts - 1,
			Reason:     reason,
			Error:      err.Error(),
			Backoff:    delay,
		},
	)
	content := fmt.Sprintf("Temporary model failure. Retrying (%d/%d)...", attempt, modelMaxAttempts-1)
	if reason == "fallback" {
		content = "Model request failed. Trying another configured model..."
	}
	if reason == "empty_answer" {
		content = "The model returned no answer. Trying once more..."
	}
	if reason == "context_limit" {
		content = "Context window exceeded. Compressing history and retrying..."
	}
	al.publishProgress(
		ctx,
		ts,
		&bus.ProgressMessage{
			RequestID: ts.turnID,
			ActionID:  fmt.Sprintf("model:%d", ts.currentIteration()),
			Kind:      bus.ProgressRetry,
			State:     bus.ProgressStarted,
		},
		content,
	)
}

func (al *AgentLoop) finishModelRetry(ctx context.Context, ts *turnState, err error) {
	state := bus.ProgressCompleted
	content := "Model request recovered."
	if err != nil {
		state = bus.ProgressFailed
		content = "Model recovery failed."
	}
	if errors.Is(err, context.Canceled) {
		content = "Model recovery canceled."
	}
	// Completing status delivery never continues the canceled model operation.
	al.publishProgress(
		context.WithoutCancel(ctx),
		ts,
		&bus.ProgressMessage{
			RequestID: ts.turnID,
			ActionID:  fmt.Sprintf("model:%d", ts.currentIteration()),
			Kind:      bus.ProgressRetry,
			State:     state,
		},
		content,
	)
}
