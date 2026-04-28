package agent

import (
	"context"
	"time"
)

const summaryLLMRetryLimit = 30

var summaryLLMRetryInterval = 2 * time.Second

func waitBeforeSummaryRetry(ctx context.Context) bool {
	if summaryLLMRetryInterval <= 0 {
		return true
	}
	timer := time.NewTimer(summaryLLMRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
