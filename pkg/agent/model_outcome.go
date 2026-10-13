package agent

import (
	"errors"
	"github.com/sipeed/picoclaw/pkg/providers"
	"strings"
)

var errEmptyAnswer = errors.New("the model returned no answer or tool call; please try again")

func hasModelOutcome(response *providers.LLMResponse) bool {
	return response != nil && (isAnswerContent(response) || len(response.ToolCalls) > 0)
}

func isAnswerContent(response *providers.LLMResponse) bool {
	return response != nil && strings.TrimSpace(response.Content) != "" && (response.ContentPhase == providers.ContentPhaseUnknown || response.ContentPhase == providers.ContentPhaseAnswer)
}
