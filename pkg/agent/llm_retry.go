package agent

import (
	"context"
	"errors"
	"strings"
)

func classifyLLMRetryableError(err error) (isTimeout bool, isContext bool, isTransientServer bool) {
	if err == nil {
		return false, false, false
	}

	errMsg := strings.ToLower(err.Error())

	isTimeout = errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errMsg, "deadline exceeded") ||
		strings.Contains(errMsg, "client.timeout") ||
		strings.Contains(errMsg, "timed out") ||
		strings.Contains(errMsg, "timeout exceeded")

	isContext = !isTimeout && (strings.Contains(errMsg, "context_length_exceeded") ||
		strings.Contains(errMsg, "context window") ||
		strings.Contains(errMsg, "maximum context length") ||
		strings.Contains(errMsg, "token limit") ||
		strings.Contains(errMsg, "too many tokens") ||
		strings.Contains(errMsg, "max_tokens") ||
		strings.Contains(errMsg, "invalidparameter") ||
		strings.Contains(errMsg, "prompt is too long") ||
		strings.Contains(errMsg, "request too large"))

	isTransientServer = !isTimeout && !isContext && (strings.Contains(errMsg, "status: 500") ||
		strings.Contains(errMsg, "status: 502") ||
		strings.Contains(errMsg, "status: 503") ||
		strings.Contains(errMsg, "status: 504") ||
		strings.Contains(errMsg, "\"status\": \"internal\"") ||
		strings.Contains(errMsg, "internal error encountered") ||
		strings.Contains(errMsg, "service unavailable") ||
		strings.Contains(errMsg, "bad gateway") ||
		strings.Contains(errMsg, "gateway timeout") ||
		strings.Contains(errMsg, "temporarily unavailable") ||
		strings.Contains(errMsg, "upstream unavailable") ||
		strings.Contains(errMsg, "temporarily overloaded") ||
		strings.Contains(errMsg, "server overloaded"))

	return isTimeout, isContext, isTransientServer
}
