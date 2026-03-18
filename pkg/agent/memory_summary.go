package agent

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func recordMemorySummary(ctx context.Context, agent *AgentInstance, sessionKey, summary string) {
	if agent == nil || agent.MemoryIndex == nil || strings.TrimSpace(summary) == "" {
		return
	}

	channel, chatID := parseMemorySessionSource(sessionKey)
	_ = agent.MemoryIndex.ReplaceSourceObservations(ctx, "session_summary", sessionKey, []memoryindex.Observation{{
		SessionKey: sessionKey,
		Channel:    channel,
		ChatID:     chatID,
		Role:       "assistant",
		SenderID:   "summary",
		Content:    "[session_summary]\n\n" + strings.TrimSpace(summary),
	}})
}

func parseMemorySessionSource(sessionKey string) (channel, chatID string) {
	parts := strings.Split(sessionKey, ":")
	if len(parts) >= 5 && parts[0] == "agent" {
		return parts[2], strings.Join(parts[4:], ":")
	}
	return "", ""
}
