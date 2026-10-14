package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// Public errors retain their source for errors.Is/As and recovery decisions,
// while ordinary formatting never prints an upstream response body.
type publicAgentError struct {
	message string
	cause   error
}

func (e *publicAgentError) Error() string { return e.message }
func (e *publicAgentError) Unwrap() error { return e.cause }

func publicProviderError(err error) error {
	if err == nil {
		return nil
	}
	message := providers.ErrorGuidance(err)
	var budget *contextBudgetError
	if errors.As(err, &budget) {
		message = budget.Error()
	} else if errors.Is(err, errModelAttemptBudget) {
		message = errModelAttemptBudget.Error()
	} else if errors.Is(err, errEmptyAnswer) {
		message = errEmptyAnswer.Error()
	}
	if message == "" {
		message = "The model request failed. Check the selected provider, model, endpoint and request settings."
	}
	return &publicAgentError{message: message, cause: err}
}

// This local diagnostic contains measured limits, never remote response text.
type contextBudgetError struct{ estimated, limit int }

func (e *contextBudgetError) Error() string {
	return fmt.Sprintf(
		"assembled request exceeds configured context window: estimated %d tokens including output reserve, limit %d; reduce the current input, instructions, tools or output limit",
		e.estimated,
		e.limit,
	)
}

func (al *AgentLoop) publicError(err error) error {
	if err == nil {
		return nil
	}
	return &publicAgentError{message: al.safeErrorText(err), cause: err}
}

// safeErrorText applies mandatory credential protection before optional filtering.
// Provider failures use repair guidance rather than untrusted response bodies.
func (al *AgentLoop) safeErrorText(err error) string {
	return safeErrorText(al.cfg, err)
}

func safeErrorText(cfg *config.Config, err error) string {
	if err == nil {
		return ""
	}
	text := providers.ErrorGuidance(err)
	if text == "" {
		text = err.Error()
	}
	for _, mc := range cfg.ModelList {
		for _, key := range append([]string{mc.APIKey}, mc.APIKeys...) {
			if key != "" {
				text = strings.ReplaceAll(text, key, "[REDACTED]")
			}
		}
	}
	return cfg.FilterSensitiveData(text)
}
