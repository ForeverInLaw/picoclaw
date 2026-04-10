package agent

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyLLMRetryableError(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantTimeout     bool
		wantContext     bool
		wantServerRetry bool
	}{
		{
			name:        "deadline exceeded is timeout",
			err:         context.DeadlineExceeded,
			wantTimeout: true,
		},
		{
			name:        "context window is context error",
			err:         errors.New("API request failed: maximum context length exceeded"),
			wantContext: true,
		},
		{
			name:            "500 internal is transient server error",
			err:             errors.New("API request failed:\n  Status: 500\n  Body: [{\"error\":{\"status\":\"INTERNAL\",\"message\":\"Internal error encountered.\"}}]"),
			wantServerRetry: true,
		},
		{
			name:            "gateway timeout is transient server error",
			err:             errors.New("API request failed: Status: 504 Body: bad gateway timeout"),
			wantServerRetry: true,
		},
		{
			name: "validation error is not retryable",
			err:  errors.New("API request failed: Status: 400 Body: invalid argument"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTimeout, gotContext, gotServerRetry := classifyLLMRetryableError(tc.err)
			if gotTimeout != tc.wantTimeout {
				t.Fatalf("timeout=%v, want %v", gotTimeout, tc.wantTimeout)
			}
			if gotContext != tc.wantContext {
				t.Fatalf("context=%v, want %v", gotContext, tc.wantContext)
			}
			if gotServerRetry != tc.wantServerRetry {
				t.Fatalf("serverRetry=%v, want %v", gotServerRetry, tc.wantServerRetry)
			}
		})
	}
}
