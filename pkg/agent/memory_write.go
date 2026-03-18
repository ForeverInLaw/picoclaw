package agent

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func recordMemoryObservation(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey, channel, chatID, role, senderID, content string,
) {
	if agent == nil || agent.MemoryIndex == nil || strings.TrimSpace(content) == "" {
		return
	}

	_ = agent.MemoryIndex.AddObservation(ctx, memoryindex.Observation{
		SessionKey: sessionKey,
		Channel:    channel,
		ChatID:     chatID,
		Role:       role,
		SenderID:   senderID,
		Content:    content,
	})
}
